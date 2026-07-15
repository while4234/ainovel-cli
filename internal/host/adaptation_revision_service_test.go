package host

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

var (
	adaptationTestVolumeID = domain.LegacyStructureID("adaptation-revision-test", domain.StructureKindVolume, "volume/1")
	adaptationTestChapter1 = domain.LegacyStructureID("adaptation-revision-test", domain.StructureKindChapter, "chapter/1")
	adaptationTestChapter2 = domain.LegacyStructureID("adaptation-revision-test", domain.StructureKindChapter, "chapter/2")
	adaptationTestAddedID  = domain.LegacyStructureID("adaptation-revision-test", domain.StructureKindChapter, "chapter/added")
)

func TestAdaptationRevisionServiceRunsFourStagesAndThreeGranularities(t *testing.T) {
	stages := []domain.ManuscriptStage{domain.ManuscriptStageProposalComplete, domain.ManuscriptStageOutlineComplete, domain.ManuscriptStageWriting, domain.ManuscriptStageComplete}
	granularities := []string{domain.AdaptationGranularityChapter, domain.AdaptationGranularityArc, domain.AdaptationGranularityFree}
	for _, stage := range stages {
		for _, granularity := range granularities {
			t.Run(string(stage)+"/"+granularity, func(t *testing.T) {
				st, base, candidate := seedAdaptationRevisionProject(t, stage, granularity, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append an original bridge chapter", Candidate: candidate}, "preview")
				if err != nil {
					t.Fatal(err)
				}
				if previewed.Preview.Stage != stage || previewed.Preview.BasePlanSignature != adaptationPlanSignature(base) {
					t.Fatalf("store-derived preview drifted: %+v", previewed.Preview)
				}
				session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
				published, err := service.Publish(*previewed.Preview, session, "publish")
				if err != nil {
					t.Fatal(err)
				}
				if published.Stage != domain.RevisionStageCompleted {
					t.Fatalf("published revision=%+v", published)
				}
				formal, err := st.Adaptation.LoadPlan()
				if err != nil || formal == nil || len(formal.Chapters) != 3 || formal.Chapters[2].ID != adaptationTestAddedID {
					t.Fatalf("formal plan was not atomically published: plan=%+v err=%v", formal, err)
				}
			})
		}
	}
}

func TestAdaptationRevisionServiceTreatsDetailsGeneratingWithOutlineProgressAsProposalComplete(t *testing.T) {
	for _, granularity := range []string{domain.AdaptationGranularityChapter, domain.AdaptationGranularityArc, domain.AdaptationGranularityFree} {
		t.Run(granularity, func(t *testing.T) {
			st, _, _ := seedAdaptationRevisionProject(t, domain.ManuscriptStageProposalComplete, granularity, false)
			if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageDetailsGenerating, -1); err != nil {
				t.Fatal(err)
			}
			stage, err := NewAdaptationRevisionService(st).CurrentManuscriptStage()
			if err != nil || stage != domain.ManuscriptStageProposalComplete {
				t.Fatalf("details-generating production state stage=%q err=%v", stage, err)
			}
		})
	}
}

func TestAdaptationRevisionServicePersistsBatchFailurePauseAndRestart(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "context overflow"); err != nil {
		t.Fatal(err)
	}
	paused, err := service.Pause(session, "pause")
	if err != nil {
		t.Fatal(err)
	}

	restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	resumed, err := restarted.Resume(paused, "resume-session")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err = restarted.RunBatchCommand(resumed.ID, domain.AdaptationRevisionBatchResume, "adaptation-batch-001", "")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.BatchPlan.Batches[0].Status != domain.BatchStatusPending || runtime.BatchPlan.Batches[0].Attempts != 1 || runtime.Paused {
		t.Fatalf("durable batch checkpoint drifted after restart: %+v", runtime)
	}
}

func TestAdaptationRevisionServiceReplaysRuntimeAndTerminalSideEffects(t *testing.T) {
	t.Run("preview detail and structure approval preserve progressed runtime", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service := NewAdaptationRevisionService(st)
		request := AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}
		previewed, err := service.Preview(request, "preview-replay")
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
		if err != nil {
			t.Fatal(err)
		}
		session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "structure")
		if err != nil {
			t.Fatal(err)
		}
		completeAdaptationRuntime(t, service, session.ID)
		evidence := adaptationPassingEvidence(session)
		audited, err := service.RecordAuditSet(session, evidence, "structure-audit")
		if err != nil {
			t.Fatal(err)
		}
		approved, err := service.ApproveStage(audited, "structure-approve-replay")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(approved.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		beforeApprovalReplay, _ := st.Adaptation.LoadRevisionRuntime()
		service.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error { return errors.New("replay must not persist runtime") }
		if replay, err := service.ApproveStage(audited, "structure-approve-replay"); err != nil || !reflect.DeepEqual(replay, approved) {
			t.Fatalf("structure approval replay=%+v err=%v", replay, err)
		}
		if replay, err := service.Preview(request, "preview-replay"); err != nil || !reflect.DeepEqual(replay, previewed) {
			t.Fatalf("preview replay=%+v err=%v", replay, err)
		}
		afterApprovalReplay, _ := st.Adaptation.LoadRevisionRuntime()
		if !reflect.DeepEqual(beforeApprovalReplay, afterApprovalReplay) {
			t.Fatal("replay reset progressed BatchPlan")
		}
		service.saveRevisionRuntime = nil
		detailed, err := service.SubmitDetailedOutlineCandidate(candidate, approved, "details-replay")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(detailed.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		beforeDetailReplay, _ := st.Adaptation.LoadRevisionRuntime()
		service.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error {
			return errors.New("replay must not replace details runtime")
		}
		if replay, err := service.SubmitDetailedOutlineCandidate(candidate, approved, "details-replay"); err != nil || !reflect.DeepEqual(replay, detailed) {
			t.Fatalf("detail replay=%+v err=%v", replay, err)
		}
		afterDetailReplay, _ := st.Adaptation.LoadRevisionRuntime()
		if !reflect.DeepEqual(beforeDetailReplay, afterDetailReplay) {
			t.Fatal("detail replay replaced progressed BatchPlan")
		}
	})

	t.Run("cancel and publish replay after runtime is gone", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service := NewAdaptationRevisionService(st)
		previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "cancel", Candidate: candidate}, "cancel-preview")
		if err != nil {
			t.Fatal(err)
		}
		cancelled, err := service.Cancel(previewed.Session, "cancel-replay")
		if err != nil {
			t.Fatal(err)
		}
		service.clearRevisionRuntime = func(string) error { return errors.New("replay must not clean runtime") }
		if replay, err := service.Cancel(previewed.Session, "cancel-replay"); err != nil || !reflect.DeepEqual(replay, cancelled) {
			t.Fatalf("cancel replay=%+v err=%v", replay, err)
		}

		st, _, candidate = seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service = NewAdaptationRevisionService(st)
		previewed, err = service.Preview(AdaptationRevisionPreviewRequest{Intent: "publish", Candidate: candidate}, "publish-preview")
		if err != nil {
			t.Fatal(err)
		}
		session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
		published, err := service.Publish(*previewed.Preview, session, "publish-replay")
		if err != nil {
			t.Fatal(err)
		}
		service.clearRevisionRuntime = func(string) error { return errors.New("replay must not clean runtime") }
		if replay, err := service.Publish(*previewed.Preview, session, "publish-replay"); err != nil || !reflect.DeepEqual(replay, published) {
			t.Fatalf("publish replay=%+v err=%v", replay, err)
		}
	})
}

func TestAdaptationRevisionReceiptFailureRollsBackAndRestartsEveryTransition(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error)
	}{
		{
			name: "preview",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				request := AdaptationRevisionPreviewRequest{Intent: "receipt failure preview", Candidate: candidate}
				return st, NewAdaptationRevisionService(st), func(service *AdaptationRevisionService) error {
					_, err := service.Preview(request, "receipt-failure-preview")
					return err
				}
			},
		},
		{
			name: "structure approval",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "receipt failure structure", Candidate: candidate}, "receipt-structure-preview")
				if err != nil {
					t.Fatal(err)
				}
				session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "receipt-structure-impact")
				if err != nil {
					t.Fatal(err)
				}
				session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "receipt-structure-submit")
				if err != nil {
					t.Fatal(err)
				}
				completeAdaptationRuntime(t, service, session.ID)
				audited, err := service.RecordAuditSet(session, adaptationPassingEvidence(session), "receipt-structure-audit")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) error {
					_, err := service.ApproveStage(audited, "receipt-failure-structure-approve")
					return err
				}
			},
		},
		{
			name: "detail submission",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "receipt failure details", Candidate: candidate}, "receipt-details-preview")
				if err != nil {
					t.Fatal(err)
				}
				session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "receipt-details-impact")
				if err != nil {
					t.Fatal(err)
				}
				session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "receipt-details-structure")
				if err != nil {
					t.Fatal(err)
				}
				completeAdaptationRuntime(t, service, session.ID)
				audited, err := service.RecordAuditSet(session, adaptationPassingEvidence(session), "receipt-details-audit")
				if err != nil {
					t.Fatal(err)
				}
				approved, err := service.ApproveStage(audited, "receipt-details-approve")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) error {
					_, err := service.SubmitDetailedOutlineCandidate(candidate, approved, "receipt-failure-details")
					return err
				}
			},
		},
		{
			name: "cancel",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "receipt failure cancel", Candidate: candidate}, "receipt-cancel-preview")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) error {
					_, err := service.Cancel(previewed.Session, "receipt-failure-cancel")
					return err
				}
			},
		},
		{
			name: "publish",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) error) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "receipt failure publish", Candidate: candidate}, "receipt-publish-preview")
				if err != nil {
					t.Fatal(err)
				}
				session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
				return st, service, func(service *AdaptationRevisionService) error {
					_, err := service.Publish(*previewed.Preview, session, "receipt-failure-publish")
					return err
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name+"/write failure", func(t *testing.T) {
			st, service, command := test.setup(t)
			before := adaptationRevisionProjectBytes(t, st.Dir())
			service.saveRevisionReceipt = func(string, string, string, any) error {
				return errors.New("injected receipt write failure")
			}
			if err := command(service); err == nil || !strings.Contains(err.Error(), "injected receipt write failure") {
				t.Fatalf("receipt failure was not returned: %v", err)
			}
			after := adaptationRevisionProjectBytes(t, st.Dir())
			if !reflect.DeepEqual(before, after) {
				t.Fatal("receipt failure did not restore the exact pre-command project snapshot")
			}
			restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
			if err := command(restarted); err != nil {
				t.Fatalf("same command was not safely retryable after restart: %v", err)
			}
		})
		t.Run(test.name+"/interrupted before receipt", func(t *testing.T) {
			st, service, command := test.setup(t)
			before := adaptationRevisionProjectBytes(t, st.Dir())
			service.saveRevisionReceipt = func(string, string, string, any) error {
				panic("simulated process interruption before receipt")
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("command did not reach the simulated interruption")
					}
				}()
				_ = command(service)
			}()
			restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
			restarted.saveRevisionReceipt = func(string, string, string, any) error {
				return errors.New("stop after restart recovery")
			}
			if err := command(restarted); err == nil || !strings.Contains(err.Error(), "stop after restart recovery") {
				t.Fatalf("restart did not reach the recovered command boundary: %v", err)
			}
			if after := adaptationRevisionProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
				t.Fatal("restart recovery did not restore the exact pre-command project snapshot")
			}
			if err := command(NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))); err != nil {
				t.Fatalf("same command was not retryable after interrupted-command recovery: %v", err)
			}
		})
	}
}

func TestAdaptationRevisionPreparedOwnershipExcludesNormalFlowAcrossRestart(t *testing.T) {
	t.Run("preview preparation", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service := NewAdaptationRevisionService(st)
		competitor := storepkg.NewStore(st.Dir())
		before := adaptationRevisionProjectBytes(t, st.Dir())
		competitorImpact, err := domain.NewRevisionImpact("competing preview revision", []domain.RevisionImpactItem{{
			ArtifactID: "chapter-1", ArtifactKind: "outline", Change: "compete",
		}})
		if err != nil {
			t.Fatal(err)
		}
		var acquireErr, revisionErr error
		service.afterCommandPrepared = func() {
			_, acquireErr = competitor.Revisions.AcquireNormalFlow("preview-race")
			_, revisionErr = competitor.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
				Intent: "competing preview", Impact: competitorImpact, IdempotencyKey: "preview-ownership-race",
			})
			panic("interrupt preview after durable preparation")
		}
		assertAdaptationCommandPanics(t, func() {
			_, _ = service.Preview(AdaptationRevisionPreviewRequest{Intent: "preview ownership race", Candidate: candidate}, "preview-ownership-race")
		})
		if !errors.Is(acquireErr, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("normal flow entered prepared preview: %v", acquireErr)
		}
		if !errors.Is(revisionErr, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("competing revision entered prepared preview: %v", revisionErr)
		}
		restartedStore := storepkg.NewStore(st.Dir())
		if after := adaptationRevisionProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
			t.Fatal("restart did not exactly roll back interrupted preview preparation")
		}
		restarted := NewAdaptationRevisionService(restartedStore)
		previewed, err := restarted.Preview(AdaptationRevisionPreviewRequest{Intent: "preview ownership race", Candidate: candidate}, "preview-ownership-race")
		if err != nil {
			t.Fatalf("recovered preview was not replayable: %v", err)
		}
		if _, err := restarted.Cancel(previewed.Session, "preview-ownership-cleanup"); err != nil {
			t.Fatal(err)
		}
		lease, err := competitor.Revisions.AcquireNormalFlow("preview-successor")
		if err != nil {
			t.Fatalf("normal flow remained fenced after terminal receipt cleanup: %v", err)
		}
		if err := competitor.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name        string
		key         string
		nonterminal bool
		setup       func(*testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) (*domain.RevisionSession, error))
	}{
		{
			name: "nonterminal approval before receipt", key: "approve-ownership-race", nonterminal: true,
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) (*domain.RevisionSession, error)) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "approval ownership race", Candidate: candidate}, "approval-ownership-preview")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) (*domain.RevisionSession, error) {
					return service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "approve-ownership-race")
				}
			},
		},
		{
			name: "cancel before receipt", key: "cancel-ownership-race",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) (*domain.RevisionSession, error)) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "cancel ownership race", Candidate: candidate}, "cancel-ownership-preview")
				if err != nil {
					t.Fatal(err)
				}
				return st, service, func(service *AdaptationRevisionService) (*domain.RevisionSession, error) {
					return service.Cancel(previewed.Session, "cancel-ownership-race")
				}
			},
		},
		{
			name: "publish before receipt", key: "publish-ownership-race",
			setup: func(t *testing.T) (*storepkg.Store, *AdaptationRevisionService, func(*AdaptationRevisionService) (*domain.RevisionSession, error)) {
				st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
				service := NewAdaptationRevisionService(st)
				previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "publish ownership race", Candidate: candidate}, "publish-ownership-preview")
				if err != nil {
					t.Fatal(err)
				}
				session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
				return st, service, func(service *AdaptationRevisionService) (*domain.RevisionSession, error) {
					return service.Publish(*previewed.Preview, session, "publish-ownership-race")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, service, command := test.setup(t)
			competitor := storepkg.NewStore(st.Dir())
			before := adaptationRevisionProjectBytes(t, st.Dir())
			var acquireErr, mutationErr error
			service.saveRevisionReceipt = func(_ string, _ string, _ string, result any) error {
				_, acquireErr = competitor.Revisions.AcquireNormalFlow(test.name)
				resultSession, ok := result.(*domain.RevisionSession)
				if !ok || resultSession == nil {
					t.Fatalf("prepared command result = %T, want revision session", result)
				}
				if test.nonterminal {
					policy, _, policyErr := service.boundPolicy(resultSession.ID)
					if policyErr != nil {
						t.Fatal(policyErr)
					}
					_, mutationErr = competitor.Revisions.Pause(policy, storepkg.RevisionMutationInput{
						SessionID: resultSession.ID, ExpectedRevision: resultSession.Revision, IdempotencyKey: test.key,
					})
				} else {
					_, mutationErr = competitor.Revisions.Start(fakeHostRevisionPolicy{}, storepkg.StartRevisionInput{
						Intent: "terminal same-key competitor", Impact: adaptationStoreFenceImpactForHost(t), IdempotencyKey: test.key,
					})
				}
				panic("interrupt terminal command before durable receipt")
			}
			assertAdaptationCommandPanics(t, func() { _, _ = command(service) })
			if !errors.Is(acquireErr, storepkg.ErrRevisionCommandInProgress) &&
				!(test.nonterminal && errors.Is(acquireErr, storepkg.ErrActiveRevisionBlocksNormalFlow)) {
				t.Fatalf("normal flow entered terminal command before receipt: %v", acquireErr)
			}
			if !errors.Is(mutationErr, storepkg.ErrRevisionCommandInProgress) {
				t.Fatalf("same-key direct mutation entered prepared command: %v", mutationErr)
			}
			restartedStore := storepkg.NewStore(st.Dir())
			if after := adaptationRevisionProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
				t.Fatal("restart recovery overwrote or failed to restore the exact terminal-command snapshot")
			}
			result, err := command(NewAdaptationRevisionService(restartedStore))
			if err != nil {
				t.Fatalf("terminal command was not replayable after recovery: %v", err)
			}
			replay, err := command(NewAdaptationRevisionService(storepkg.NewStore(st.Dir())))
			if err != nil || !reflect.DeepEqual(replay, result) {
				t.Fatalf("durable terminal receipt did not replay: replay=%+v result=%+v err=%v", replay, result, err)
			}
			if test.nonterminal {
				successorService := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
				policy, _, policyErr := successorService.boundPolicy(result.ID)
				if policyErr != nil {
					t.Fatal(policyErr)
				}
				paused, pauseErr := competitor.Revisions.Pause(policy, storepkg.RevisionMutationInput{
					SessionID: result.ID, ExpectedRevision: result.Revision, IdempotencyKey: test.key + "-successor",
				})
				if pauseErr != nil {
					t.Fatalf("successor mutation remained fenced after nonterminal receipt cleanup: %v", pauseErr)
				}
				if _, err := successorService.Cancel(paused, test.key+"-cleanup"); err != nil {
					t.Fatal(err)
				}
				return
			}
			lease, err := competitor.Revisions.AcquireNormalFlow(test.name + " successor")
			if err != nil {
				t.Fatalf("normal flow remained fenced after terminal receipt cleanup: %v", err)
			}
			if err := competitor.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAdaptationRevisionPreparedOwnerGuardsTerminalReceiptAndRuntime(t *testing.T) {
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "guard terminal durability", Candidate: candidate}, "durability-preview")
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := st.Adaptation.LoadRevisionRuntime()
	if err != nil || runtime == nil {
		t.Fatalf("preview runtime missing: runtime=%+v err=%v", runtime, err)
	}
	competitor := storepkg.NewStore(st.Dir())
	otherProject := storepkg.NewStore(t.TempDir())
	formalSnapshot, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil {
		t.Fatalf("load progress: progress=%+v err=%v", progress, err)
	}
	layered := preparedOwnerLayeredFixture("host-formal-owner")
	var staleOwner *storepkg.RevisionStore
	if err := st.WithPreparedAdaptationRevisionCommand("host-stale-formal", "publish", "host-stale-formal-fingerprint", func(owner *storepkg.RevisionStore) error {
		staleOwner = owner
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key := "guarded-cancel"
	request := adaptationCommandReceiptRequest("cancel", previewed.Session.Revision)
	operation, payload, err := adaptationRevisionCommandReceiptIdentity(request)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := domain.ContentSignature(encoded)
	var receiptErr, saveErr, clearErr error
	var formalErrors []error
	var formalBytesBefore, formalBytesAfter map[string][]byte
	service.SetCommandPreparedHookForTesting(func() {
		formalBytesBefore = adaptationRevisionProjectBytes(t, st.Dir())
		receiptErr = competitor.SaveAdaptationRevisionServiceReceipt(competitor.Revisions, key, operation, fingerprint, previewed.Session)
		corrupted := *runtime
		corrupted.Paused = true
		saveErr = competitor.SaveAdaptationRevisionRuntime(competitor.Revisions, corrupted)
		clearErr = competitor.ClearAdaptationRevisionRuntime(competitor.Revisions, runtime.SessionID)
		attempts := []func() error{
			func() error {
				return competitor.SaveAdaptationPlanForRevision(competitor.Revisions, candidate, previewed.Session.ID)
			},
			func() error {
				return competitor.RestoreAdaptationPlanForRevision(competitor.Revisions, base, previewed.Session.ID)
			},
			func() error {
				return competitor.RestoreAdaptationFormalSnapshot(competitor.Revisions, formalSnapshot, previewed.Session.ID)
			},
			func() error {
				return competitor.ClearAdaptationRevisionAudits(competitor.Revisions, previewed.Session.ID)
			},
			func() error {
				return competitor.SaveAdaptationRevisionProgress(competitor.Revisions, progress, previewed.Session.ID)
			},
			func() error {
				return st.SaveAdaptationPlanForRevision(otherProject.Revisions, candidate, previewed.Session.ID)
			},
			func() error { return st.SaveAdaptationPlanForRevision(staleOwner, candidate, previewed.Session.ID) },
			func() error {
				return st.PublishLayeredStructureForRevision(nil, layered, "host-forged-layered-publish")
			},
			func() error { return st.RestoreLayeredStructureForRevision(nil, layered, progress) },
		}
		formalErrors = make([]error, len(attempts))
		var wg sync.WaitGroup
		for index := range attempts {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				formalErrors[index] = attempts[index]()
			}(index)
		}
		wg.Wait()
		formalBytesAfter = adaptationRevisionProjectBytes(t, st.Dir())
	})
	cancelled, err := service.Cancel(previewed.Session, key)
	if err != nil {
		t.Fatal(err)
	}
	for name, forged := range map[string]error{"matching receipt": receiptErr, "runtime save": saveErr, "runtime clear": clearErr} {
		if !errors.Is(forged, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("forged %s bypassed prepared owner: %v", name, forged)
		}
	}
	for index, forged := range formalErrors {
		if !errors.Is(forged, storepkg.ErrRevisionCommandInProgress) {
			t.Fatalf("forged Host formal write %d bypassed prepared owner: %v", index, forged)
		}
	}
	if !reflect.DeepEqual(formalBytesBefore, formalBytesAfter) {
		t.Fatal("rejected Host same-session/cross-project/stale races changed project bytes")
	}
	if active, err := st.Revisions.Active(); err != nil || active != nil {
		t.Fatalf("terminal owner did not clear active session: active=%+v err=%v", active, err)
	}
	if persisted, err := st.Adaptation.LoadRevisionRuntime(); err != nil || persisted != nil {
		t.Fatalf("terminal owner did not clear runtime: runtime=%+v err=%v", persisted, err)
	}
	lease, err := competitor.Revisions.AcquireNormalFlow("terminal-owner-successor")
	if err != nil {
		t.Fatalf("terminal receipt cleanup did not release successor: %v", err)
	}
	if err := competitor.Revisions.ReleaseNormalFlow(lease.Token); err != nil {
		t.Fatal(err)
	}
	service.SetCommandPreparedHookForTesting(nil)
	replayed, err := service.Cancel(previewed.Session, key)
	if err != nil || !reflect.DeepEqual(replayed, cancelled) {
		t.Fatalf("terminal receipt replay drifted: replay=%+v want=%+v err=%v", replayed, cancelled, err)
	}
}

func assertAdaptationCommandPanics(t *testing.T, command func()) {
	t.Helper()
	panicked := false
	func() {
		defer func() { panicked = recover() != nil }()
		command()
	}()
	if !panicked {
		t.Fatal("command did not reach the simulated process interruption")
	}
}

func adaptationStoreFenceImpactForHost(t *testing.T) domain.RevisionImpact {
	t.Helper()
	impact, err := domain.NewRevisionImpact("same-key prepared command competitor", []domain.RevisionImpactItem{{
		ArtifactID: "chapter-1", ArtifactKind: "outline", Change: "compete",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return impact
}

func preparedOwnerLayeredFixture(project string) []domain.VolumeOutline {
	volumeID := domain.LegacyStructureID(project, domain.StructureKindVolume, "volume-1")
	arcID := domain.LegacyStructureID(project, domain.StructureKindArc, "volume-1/arc-1")
	chapterID := domain.LegacyStructureID(project, domain.StructureKindChapter, "volume-1/arc-1/chapter-1")
	return []domain.VolumeOutline{{
		ID: volumeID, Index: 1, Title: "Volume", Theme: "theme",
		Arcs: []domain.ArcOutline{{
			ID: arcID, Index: 1, Title: "Arc", Goal: "goal",
			Chapters: []domain.OutlineEntry{{ID: chapterID, Chapter: 1, Title: "One", CoreEvent: "event", Hook: "hook", Scenes: []string{"scene"}}},
		}},
	}}
}

func TestAdaptationRevisionHostReadCannotRecoverPendingMigration(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	if _, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "fence Host read", Candidate: candidate}, "host-read-preview"); err != nil {
		t.Fatal(err)
	}
	migrationLog := filepath.Join(st.Dir(), "meta", "structure", "migration.json")
	if err := os.MkdirAll(filepath.Dir(migrationLog), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(migrationLog, []byte(`{"version":1,"stage":"planned"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before := adaptationRevisionProjectBytes(t, st.Dir())
	reopened := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	if _, err := reopened.CurrentManuscriptStage(); err == nil || !strings.Contains(err.Error(), "active revision") {
		t.Fatalf("Host read recovered a pending migration through revision ownership: %v", err)
	}
	if after := adaptationRevisionProjectBytes(t, st.Dir()); !reflect.DeepEqual(before, after) {
		t.Fatal("rejected Host read changed the pending formal/derived snapshot")
	}
}

func adaptationRevisionProjectBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".lock") || strings.Contains(rel, "adaptation-command-journal") || strings.Contains(rel, "adaptation-command-snapshot") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result[rel] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func adaptationPassingEvidence(session *domain.RevisionSession) []domain.RevisionAuditEvidence {
	evidence := make([]domain.RevisionAuditEvidence, 0, len(session.AuditExpectations))
	for _, expected := range session.AuditExpectations {
		evidence = append(evidence, domain.RevisionAuditEvidence{Scope: expected.Scope, ScopeID: expected.ScopeID, FromChapter: expected.FromChapter, ToChapter: expected.ToChapter, ContentSignature: expected.ContentSignature, Passed: true})
	}
	return evidence
}

func TestAdaptationRevisionBatchPlanUsesImmutableBoundedContext(t *testing.T) {
	base, manifest := adaptationRevisionServiceFixture(domain.AdaptationGranularityArc)
	base.Chapters[0].SourceSegments = nil
	base.Chapters[1].SourceSegments = nil
	base.Chapters[0].SourceRunes = 999999
	base.Chapters[1].SourceRunes = 1
	manifest.Chapters[0].Runes = 6000
	manifest.Chapters[1].Runes = 6000
	impact, err := domain.NewRevisionImpact("two risky chapters", []domain.RevisionImpactItem{
		{ArtifactID: base.Chapters[0].ID, ArtifactKind: domain.StructureKindChapter, Change: "revise", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"one"}},
		{ArtifactID: base.Chapters[1].ID, ArtifactKind: domain.StructureKindChapter, Change: "revise", Requirement: domain.StructureImpactRequired, Cause: domain.StructureImpactContentDependency, DependencyEvidence: []string{"two"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deriveAdaptationRevisionBatchPlan(base, impact, &manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Batches) != 2 || plan.Batches[0].ContextUnits > domain.AdaptationRevisionBatchContextMaxUnits || plan.Batches[1].ContextUnits > domain.AdaptationRevisionBatchContextMaxUnits {
		t.Fatalf("still-risky pair was not iteratively split: %+v", plan.Batches)
	}
	if plan.Batches[0].Context[0].Units != 6000 || strings.Contains(plan.Batches[0].Context[0].ID, "source:2") {
		t.Fatalf("batch context trusted forged runes or loaded non-local source: %+v", plan.Batches[0])
	}
	manifest.Chapters[0].Runes = domain.AdaptationRevisionBatchContextMaxUnits + 1
	hugeImpact, err := domain.NewRevisionImpact("huge single source", impact.Items[:1])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deriveAdaptationRevisionBatchPlan(base, hugeImpact, &manifest); err == nil {
		t.Fatal("huge single immutable source context was accepted")
	}
}

func TestAdaptationRevisionServiceAllowsOnlyWhollyUnwrittenMoves(t *testing.T) {
	for _, test := range []struct {
		name        string
		markWritten func(*testing.T, *storepkg.Store)
		wantError   bool
	}{
		{name: "unwritten move allowed"},
		{name: "completed number without final body is written", wantError: true, markWritten: func(t *testing.T, st *storepkg.Store) {
			progress, _ := st.Progress.Load()
			progress.CompletedChapters = []int{2}
			if err := st.Progress.Save(progress); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "non-empty partial draft is written", wantError: true, markWritten: func(t *testing.T, st *storepkg.Store) {
			if err := st.Drafts.SaveDraft(2, "partial real draft"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
			if test.markWritten != nil {
				test.markWritten(t, st)
			}
			moved := adaptationRevisionTestClone(t, candidate)
			moved.TargetEventLedger[0].DependsOn = []string{"source-event-1"}
			moved.Chapters[1], moved.Chapters[2] = moved.Chapters[2], moved.Chapters[1]
			for index := range moved.Chapters {
				moved.Chapters[index].Chapter = index + 1
				moved.Chapters[index].OutlineEntry.Chapter = index + 1
			}
			service := NewAdaptationRevisionService(st)
			previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "move target", Candidate: moved}, "move")
			if test.wantError && (err == nil || !strings.Contains(err.Error(), "cannot be moved")) {
				t.Fatalf("written move was not rejected: %v", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("wholly unwritten move was rejected: %v", err)
			}
			if !test.wantError {
				session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, moved)
				if _, err := service.Publish(*previewed.Preview, session, "publish"); err != nil {
					t.Fatalf("wholly unwritten move could not be published: %v", err)
				}
			}
		})
	}
}

func TestAdaptationRevisionDetailedOutlineCannotReplaceSealedOwnership(t *testing.T) {
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityChapter, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "structure")
	if err != nil {
		t.Fatal(err)
	}
	completeAdaptationRuntime(t, service, session.ID)
	session = passAdaptationAuditAndApprove(t, service, session, "structure")

	mutations := map[string]func(*domain.AdaptationPlan){
		"source chapters": func(plan *domain.AdaptationPlan) { plan.Chapters[0].SourceChapters = []int{2} },
		"source segments": func(plan *domain.AdaptationPlan) { plan.Chapters[0].SourceSegments[0].SourceChapter = 2 },
		"source event owner": func(plan *domain.AdaptationPlan) {
			plan.Chapters[0].EventIDs, plan.Chapters[1].EventIDs = plan.Chapters[1].EventIDs, plan.Chapters[0].EventIDs
		},
		"added event owner": func(plan *domain.AdaptationPlan) {
			plan.Chapters[2].AddedEventIDs = nil
			plan.Chapters[1].AddedEventIDs = []string{"added-event"}
		},
		"coverage":           func(plan *domain.AdaptationPlan) { plan.Chapters[0].CoverageNote = "replaced coverage" },
		"volume ownership":   func(plan *domain.AdaptationPlan) { plan.Volumes[0].TargetFrom = 2 },
		"protected contract": func(plan *domain.AdaptationPlan) { plan.Chapters[0].ForbiddenMoves = []string{"changed"} },
		"target event ledger": func(plan *domain.AdaptationPlan) {
			plan.TargetEventLedger[0].Origin = domain.AdaptationEventOriginSource
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			adversarial := adaptationRevisionTestClone(t, candidate)
			mutate(&adversarial)
			if _, err := service.SubmitDetailedOutlineCandidate(adversarial, session, "details-"+strings.ReplaceAll(name, " ", "-")); err == nil || !strings.Contains(err.Error(), "accepted structure skeleton") {
				t.Fatalf("sealed ownership substitution was not rejected: %v", err)
			}
		})
	}
	formal, _ := st.Adaptation.LoadPlan()
	active, _ := st.Revisions.Active()
	if !reflect.DeepEqual(*formal, base) || active == nil || active.Revision != session.Revision || active.Stage != domain.RevisionStageCandidateGenerating {
		t.Fatalf("rejected detail changed formal/accepted state: formal=%+v active=%+v", formal, active)
	}
}

func TestAdaptationRevisionPublishMergesRewritesAndPreservesReopenState(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageComplete, domain.AdaptationGranularityArc, true)
	progress, _ := st.Progress.Load()
	progress.PendingRewrites = []int{2}
	progress.RewriteReason = "existing repair"
	progress.ReopenedFromComplete = true
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	candidate = adaptationRevisionTestClone(t, candidate)
	candidate.Chapters[0].CoreEvent = "revised written event"
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "revise written target", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
	session, err = service.SubmitProseReworkCandidate(session, "prose")
	if err != nil {
		t.Fatal(err)
	}
	session = passAdaptationAuditAndApprove(t, service, session, "prose")
	if _, err := service.Publish(*previewed.Preview, session, "publish"); err != nil {
		t.Fatal(err)
	}
	progress, _ = st.Progress.Load()
	if !reflect.DeepEqual(progress.PendingRewrites, []int{2, 1}) || progress.Phase != domain.PhaseWriting || !progress.ReopenedFromComplete || progress.Flow != domain.FlowRewriting || !strings.Contains(progress.RewriteReason, "existing repair") {
		t.Fatalf("published progress did not preserve/merge rewrite state: %+v", progress)
	}
}

func TestAdaptationRevisionRuntimeTransitionsAreRollbackSafeAndSerialized(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
	if err != nil {
		t.Fatal(err)
	}
	session, err = service.SubmitStructureCandidate(*previewed.Preview, session, "structure")
	if err != nil {
		t.Fatal(err)
	}
	completeAdaptationRuntime(t, service, session.ID)
	session = passAdaptationAuditAndApprove(t, service, session, "structure")
	before, _ := st.Adaptation.LoadRevisionRuntime()
	stale := *session
	stale.Revision--
	if _, err := service.SubmitDetailedOutlineCandidate(candidate, &stale, "stale-details"); err == nil {
		t.Fatal("stale candidate submission unexpectedly succeeded")
	}
	after, _ := st.Adaptation.LoadRevisionRuntime()
	if !reflect.DeepEqual(before.BatchPlan, after.BatchPlan) {
		t.Fatalf("failed candidate left a new BatchPlan: before=%+v after=%+v", before.BatchPlan, after.BatchPlan)
	}

	service.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error {
		return errors.New("injected runtime persistence failure")
	}
	if _, err := service.Pause(session, "pause-fail"); err == nil {
		t.Fatal("pause persistence failure was not returned")
	}
	active, _ := st.Revisions.Active()
	after, _ = st.Adaptation.LoadRevisionRuntime()
	if active.Stage == domain.RevisionStagePaused || after.Paused {
		t.Fatalf("failed pause split session/runtime: active=%+v runtime=%+v", active, after)
	}
	service.saveRevisionRuntime = nil
	paused, err := service.Pause(session, "pause")
	if err != nil {
		t.Fatal(err)
	}
	service.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error {
		return errors.New("injected runtime persistence failure")
	}
	if _, err := service.Resume(paused, "resume-fail"); err == nil {
		t.Fatal("resume persistence failure was not returned")
	}
	active, _ = st.Revisions.Active()
	after, _ = st.Adaptation.LoadRevisionRuntime()
	if active.Stage != domain.RevisionStagePaused || !after.Paused {
		t.Fatalf("failed resume split session/runtime: active=%+v runtime=%+v", active, after)
	}
	restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	resumed, err := restarted.Resume(active, "resume-after-restart")
	if err != nil || resumed.Stage == domain.RevisionStagePaused {
		t.Fatalf("restart resume failed: resumed=%+v err=%v", resumed, err)
	}
	failed, err := restarted.Fail(resumed, "model failure", "fail-session")
	if err != nil || failed.Stage != domain.RevisionStageFailed {
		t.Fatalf("persist failure checkpoint: failed=%+v err=%v", failed, err)
	}
	restarted = NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	resumed, err = restarted.Resume(failed, "resume-failed-after-restart")
	if err != nil || resumed.Stage == domain.RevisionStageFailed {
		t.Fatalf("restart resume from failure failed: resumed=%+v err=%v", resumed, err)
	}

	if _, err := restarted.RunBatchCommand(resumed.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, commandErr := restarted.RunBatchCommand(resumed.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "concurrent failure")
			results <- commandErr
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	for commandErr := range results {
		if commandErr == nil {
			successes++
		}
	}
	after, _ = st.Adaptation.LoadRevisionRuntime()
	if successes != 1 || after.BatchPlan.Batches[0].Attempts != 1 || after.BatchPlan.Batches[0].Status != domain.BatchStatusFailed {
		t.Fatalf("concurrent checkpoint commands lost status/attempts: successes=%d runtime=%+v", successes, after.BatchPlan.Batches[0])
	}
}

func TestAdaptationRevisionTwoServiceRacesDoNotResurrectRuntimeOrLoseAttempts(t *testing.T) {
	t.Run("approve preserves concurrent batch attempt", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		first := NewAdaptationRevisionService(st)
		previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.RunBatchCommand(previewed.Session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
		batchErr, approveErr := runConcurrentAdaptationCommands(
			func() error {
				_, err := first.RunBatchCommand(previewed.Session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "model failure")
				return err
			},
			func() error {
				_, err := second.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "approve")
				return err
			},
		)
		if batchErr != nil || approveErr != nil {
			t.Fatalf("serialized approve/batch commands failed: batch=%v approve=%v", batchErr, approveErr)
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		if runtime == nil || runtime.BatchPlan.Batches[0].Attempts != 1 || runtime.BatchPlan.Batches[0].Status != domain.BatchStatusFailed || active == nil || active.Revision != previewed.Session.Revision+1 {
			t.Fatalf("approve race lost batch/session state: runtime=%+v active=%+v", runtime, active)
		}
	})

	for _, transition := range []struct {
		name string
		run  func(*AdaptationRevisionService, *domain.RevisionSession) error
		want domain.RevisionStage
	}{
		{name: "pause", want: domain.RevisionStagePaused, run: func(service *AdaptationRevisionService, session *domain.RevisionSession) error {
			_, err := service.Pause(session, "pause")
			return err
		}},
		{name: "fail", want: domain.RevisionStageFailed, run: func(service *AdaptationRevisionService, session *domain.RevisionSession) error {
			_, err := service.Fail(session, "session failure", "fail")
			return err
		}},
	} {
		t.Run(transition.name+" serializes batch attempt", func(t *testing.T) {
			st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
			first := NewAdaptationRevisionService(st)
			previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
			if err != nil {
				t.Fatal(err)
			}
			session, err := first.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
				t.Fatal(err)
			}
			second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
			batchErr, transitionErr := runConcurrentAdaptationCommands(
				func() error {
					_, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "batch failure")
					return err
				},
				func() error { return transition.run(second, session) },
			)
			if transitionErr != nil {
				t.Fatalf("%s transition failed: %v", transition.name, transitionErr)
			}
			runtime, _ := st.Adaptation.LoadRevisionRuntime()
			active, _ := st.Revisions.Active()
			wantStatus := domain.BatchStatusGenerating
			if batchErr == nil {
				wantStatus = domain.BatchStatusFailed
			}
			if runtime == nil || !runtime.Paused || runtime.BatchPlan.Batches[0].Attempts != 1 || runtime.BatchPlan.Batches[0].Status != wantStatus || active == nil || active.Stage != transition.want {
				t.Fatalf("%s race split state: batchErr=%v runtime=%+v active=%+v", transition.name, batchErr, runtime, active)
			}
		})
	}

	t.Run("cancel cannot be followed by stale runtime save", func(t *testing.T) {
		st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		first := NewAdaptationRevisionService(st)
		previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		session, err := first.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
		_, cancelErr := runConcurrentAdaptationCommands(
			func() error {
				_, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "batch failure")
				return err
			},
			func() error {
				_, err := second.Cancel(session, "cancel")
				return err
			},
		)
		if cancelErr != nil {
			t.Fatal(cancelErr)
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		formal, _ := st.Adaptation.LoadPlan()
		if runtime != nil || active != nil || formal == nil || !reflect.DeepEqual(*formal, base) {
			t.Fatalf("cancel race resurrected runtime or drifted formal state: runtime=%+v active=%+v formal=%+v", runtime, active, formal)
		}
	})

	t.Run("cancel runtime cleanup failure keeps the session active", func(t *testing.T) {
		st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		service := NewAdaptationRevisionService(st)
		previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		session, err := service.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
		if err != nil {
			t.Fatal(err)
		}
		service.clearRevisionRuntime = func(string) error { return errors.New("injected runtime cleanup failure") }
		if _, err := service.Cancel(session, "cancel"); err == nil {
			t.Fatal("cancel ignored runtime cleanup failure")
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		formal, _ := st.Adaptation.LoadPlan()
		if runtime == nil || active == nil || active.ID != session.ID || active.Stage != session.Stage || formal == nil || !reflect.DeepEqual(*formal, base) {
			t.Fatalf("failed cancel split session/runtime/formal state: runtime=%+v active=%+v formal=%+v", runtime, active, formal)
		}
	})

	t.Run("failed runtime save cannot overwrite pause", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		first := NewAdaptationRevisionService(st)
		previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		session, err := first.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchStart, "adaptation-batch-001", ""); err != nil {
			t.Fatal(err)
		}
		first.saveRevisionRuntime = func(domain.AdaptationRevisionRuntime) error { return errors.New("injected runtime save failure") }
		second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
		batchErr, pauseErr := runConcurrentAdaptationCommands(
			func() error {
				_, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "not persisted")
				return err
			},
			func() error {
				_, err := second.Pause(session, "pause")
				return err
			},
		)
		if batchErr == nil || pauseErr != nil {
			t.Fatalf("persistence race results: batch=%v pause=%v", batchErr, pauseErr)
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		if runtime == nil || !runtime.Paused || runtime.BatchPlan.Batches[0].Attempts != 1 || runtime.BatchPlan.Batches[0].Status != domain.BatchStatusGenerating || active == nil || active.Stage != domain.RevisionStagePaused {
			t.Fatalf("failed runtime save split or resurrected state: runtime=%+v active=%+v", runtime, active)
		}
	})

	t.Run("publish cannot be followed by stale runtime save", func(t *testing.T) {
		st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
		first := NewAdaptationRevisionService(st)
		previewed, err := first.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
		if err != nil {
			t.Fatal(err)
		}
		session := runAdaptationStructureAndDetailApproval(t, first, *previewed.Preview, previewed.Session, candidate)
		second := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
		_, publishErr := runConcurrentAdaptationCommands(
			func() error {
				_, err := first.RunBatchCommand(session.ID, domain.AdaptationRevisionBatchFail, "adaptation-batch-001", "stale failure")
				return err
			},
			func() error {
				_, err := second.Publish(*previewed.Preview, session, "publish")
				return err
			},
		)
		if publishErr != nil {
			t.Fatal(publishErr)
		}
		runtime, _ := st.Adaptation.LoadRevisionRuntime()
		active, _ := st.Revisions.Active()
		formal, _ := st.Adaptation.LoadPlan()
		if runtime != nil || active != nil || formal == nil || len(formal.Chapters) != len(candidate.Chapters) {
			t.Fatalf("publish race resurrected runtime or lost formal publish: runtime=%+v active=%+v formal=%+v", runtime, active, formal)
		}
	})
}

func runConcurrentAdaptationCommands(left, right func() error) (error, error) {
	start := make(chan struct{})
	results := make(chan struct {
		index int
		err   error
	}, 2)
	for index, command := range []func() error{left, right} {
		go func(index int, command func() error) {
			<-start
			results <- struct {
				index int
				err   error
			}{index: index, err: command()}
		}(index, command)
	}
	close(start)
	errs := make([]error, 2)
	for range 2 {
		result := <-results
		errs[result.index] = result.err
	}
	return errs[0], errs[1]
}

func TestAdaptationRevisionServiceRejectsWrittenMoveAndQueuesExactRework(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, true)
	service := NewAdaptationRevisionService(st)
	moved := adaptationRevisionTestClone(t, candidate)
	moved.Chapters[0], moved.Chapters[1] = moved.Chapters[1], moved.Chapters[0]
	for index := range moved.Chapters {
		moved.Chapters[index].Chapter = index + 1
		moved.Volumes[0].TargetTo = len(moved.Chapters)
	}
	if _, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "move written chapter", Candidate: moved}, "move"); err == nil || !strings.Contains(err.Error(), "cannot be moved") {
		t.Fatalf("written move was not structurally rejected: %v", err)
	}

	candidate = adaptationRevisionTestClone(t, candidate)
	candidate.Chapters[0].CoreEvent = "revised exact written event"
	candidate.Chapters[0].OutlineEntry.CoreEvent = candidate.Chapters[0].CoreEvent
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "revise exact written chapter", Candidate: candidate}, "rework-preview")
	if err != nil {
		t.Fatal(err)
	}
	session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
	if adaptationServiceApprovalStage(*session) != domain.AdaptationApprovalProse {
		t.Fatalf("missing exact prose approval stage: %+v", session)
	}
	session, err = service.SubmitProseReworkCandidate(session, "prose")
	if err != nil {
		t.Fatal(err)
	}
	if len(session.AuditExpectations) != 2 || session.AuditExpectations[0].ScopeID != adaptationTestChapter1 {
		t.Fatalf("prose intents are not exact stable-ID scopes: %+v", session.AuditExpectations)
	}
}

func TestAdaptationRevisionServiceExcludesConcurrentRevisionUntilPublishReceipt(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageComplete, domain.AdaptationGranularityFree, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "complete-book expansion", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
	policy, _, err := service.boundPolicy(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	var pauseErr error
	service.beforeRevisionCommit = func() {
		_, pauseErr = st.Revisions.Pause(policy, storepkg.RevisionMutationInput{SessionID: session.ID, ExpectedRevision: session.Revision, IdempotencyKey: "concurrent-pause"})
	}
	if _, err := service.Publish(*previewed.Preview, session, "publish"); err != nil {
		t.Fatalf("fenced publication failed: %v", err)
	}
	if !errors.Is(pauseErr, storepkg.ErrRevisionCommandInProgress) {
		t.Fatalf("competing revision entered publication before its receipt: %v", pauseErr)
	}
	formal, _ := st.Adaptation.LoadPlan()
	active, _ := st.Revisions.Active()
	if formal == nil || len(formal.Chapters) != len(candidate.Chapters) ||
		formal.Chapters[len(formal.Chapters)-1].ID != candidate.Chapters[len(candidate.Chapters)-1].ID || active != nil {
		t.Fatalf("fenced publication did not commit exactly: formal=%+v active=%+v", formal, active)
	}
}

func TestAdaptationRevisionPublishRuntimeCleanupFailureRollsBackBeforeCommit(t *testing.T) {
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageComplete, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "complete-book expansion", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	session := runAdaptationStructureAndDetailApproval(t, service, *previewed.Preview, previewed.Session, candidate)
	progressBefore, _ := st.Progress.Load()
	service.clearRevisionRuntime = func(string) error { return errors.New("injected runtime cleanup failure") }
	if _, err := service.Publish(*previewed.Preview, session, "publish"); err == nil {
		t.Fatal("publish ignored runtime cleanup failure")
	}
	formal, _ := st.Adaptation.LoadPlan()
	progressAfter, _ := st.Progress.Load()
	runtime, _ := st.Adaptation.LoadRevisionRuntime()
	active, _ := st.Revisions.Active()
	if formal == nil || !reflect.DeepEqual(*formal, base) || !reflect.DeepEqual(progressAfter, progressBefore) || runtime == nil || active == nil || active.ID != session.ID || active.Revision != session.Revision {
		t.Fatalf("runtime cleanup failure split publish state: formal=%+v progress=%+v runtime=%+v active=%+v", formal, progressAfter, runtime, active)
	}
}

func TestAdaptationRevisionServiceBlocksLegacyWritesAndNormalProjects(t *testing.T) {
	normal := storepkg.NewStore(t.TempDir())
	if _, err := NewAdaptationRevisionService(normal).CurrentManuscriptStage(); err == nil {
		t.Fatal("adaptation service accepted a normal project")
	}
	st, base, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityChapter, false)
	service := NewAdaptationRevisionService(st)
	if _, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview"); err != nil {
		t.Fatal(err)
	}
	formalBefore, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{store: st}
	if _, err := h.Rollback(domain.RollbackRequest{Confirm: true}); err == nil {
		t.Fatal("Host rollback bypassed the active adaptation revision")
	}
	formalAfter, err := st.CaptureAdaptationFormalSnapshot()
	if err != nil || !reflect.DeepEqual(formalBefore, formalAfter) {
		t.Fatalf("rejected Host rollback changed formal state: err=%v", err)
	}
	if err := st.Adaptation.SavePlan(base); err == nil || !strings.Contains(err.Error(), "blocked by active revision") {
		t.Fatalf("legacy formal plan write was not blocked: %v", err)
	}
	if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageConfirmed, -1); err == nil || !strings.Contains(err.Error(), "blocked by active revision") {
		t.Fatalf("legacy workflow write was not blocked: %v", err)
	}
	if err := st.Adaptation.SaveSourceManifest(domain.AdaptationSourceManifest{}); err == nil || !strings.Contains(err.Error(), "blocked by active revision") {
		t.Fatalf("immutable source replacement was not blocked: %v", err)
	}
	for operation, mutate := range map[string]func() error{
		"reset":                st.Adaptation.Reset,
		"reset generated":      st.Adaptation.ResetGenerated,
		"delete check":         func() error { return st.Adaptation.DeleteCheck(1) },
		"clear source batches": st.Adaptation.ClearSourceFoundationBatches,
	} {
		if err := mutate(); err == nil || !strings.Contains(err.Error(), "blocked by active revision") {
			t.Fatalf("legacy %s bypass was not blocked: %v", operation, err)
		}
	}
}

func TestAdaptationRevisionServiceRejectsRestartAfterManifestTamper(t *testing.T) {
	st, _, candidate := seedAdaptationRevisionProject(t, domain.ManuscriptStageWriting, domain.AdaptationGranularityArc, false)
	service := NewAdaptationRevisionService(st)
	previewed, err := service.Preview(AdaptationRevisionPreviewRequest{Intent: "append bridge", Candidate: candidate}, "preview")
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := st.Adaptation.LoadSourceManifest()
	manifest.Chapters[0].SHA256 = "tampered"
	payload, _ := json.MarshalIndent(manifest, "", "  ")
	path := filepath.Join(st.Dir(), "meta", "adaptation", "source_manifest.json")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	restarted := NewAdaptationRevisionService(storepkg.NewStore(st.Dir()))
	if _, err := restarted.ApproveImpact(previewed.Session.ID, previewed.Session.Revision, "impact"); err == nil || !strings.Contains(err.Error(), "restart binding") {
		t.Fatalf("manifest drift was not rejected after restart: %v", err)
	}
}

func runAdaptationStructureAndDetailApproval(t *testing.T, service *AdaptationRevisionService, preview AdaptationStructureRevisionPreview, session *domain.RevisionSession, candidate domain.AdaptationPlan) *domain.RevisionSession {
	t.Helper()
	var err error
	session, err = service.ApproveImpact(session.ID, session.Revision, "impact")
	if err != nil {
		t.Fatal(err)
	}
	if adaptationServiceApprovalStage(*session) == domain.AdaptationApprovalStructure {
		session, err = service.SubmitStructureCandidate(preview, session, "structure")
		if err != nil {
			t.Fatal(err)
		}
		completeAdaptationRuntime(t, service, session.ID)
		session = passAdaptationAuditAndApprove(t, service, session, "structure")
	}
	if adaptationServiceApprovalStage(*session) == domain.AdaptationApprovalOutline {
		session, err = service.SubmitDetailedOutlineCandidate(candidate, session, "details")
		if err != nil {
			t.Fatal(err)
		}
		completeAdaptationRuntime(t, service, session.ID)
		session = passAdaptationAuditAndApprove(t, service, session, "details")
	}
	return session
}

func completeAdaptationRuntime(t *testing.T, service *AdaptationRevisionService, sessionID string) {
	t.Helper()
	runtime, err := service.store.Adaptation.LoadRevisionRuntime()
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range runtime.BatchPlan.Batches {
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionBatchStart, batch.ID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionBatchGenerated, batch.ID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionBatchAuditPass, batch.ID, "passed"); err != nil {
			t.Fatal(err)
		}
	}
	for _, review := range runtime.BatchPlan.VolumeReviews {
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionVolumeReviewStart, review.ScopeID, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionVolumeReviewPass, review.ScopeID, "passed"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionGlobalReviewStart, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunBatchCommand(sessionID, domain.AdaptationRevisionGlobalReviewPass, "", "passed"); err != nil {
		t.Fatal(err)
	}
}

func passAdaptationAuditAndApprove(t *testing.T, service *AdaptationRevisionService, session *domain.RevisionSession, prefix string) *domain.RevisionSession {
	t.Helper()
	evidence := make([]domain.RevisionAuditEvidence, 0, len(session.AuditExpectations))
	for _, expected := range session.AuditExpectations {
		evidence = append(evidence, domain.RevisionAuditEvidence{Scope: expected.Scope, ScopeID: expected.ScopeID, FromChapter: expected.FromChapter, ToChapter: expected.ToChapter, ContentSignature: expected.ContentSignature, Passed: true})
	}
	audited, err := service.RecordAuditSet(session, evidence, prefix+"-audit")
	if err != nil {
		t.Fatal(err)
	}
	approved, err := service.ApproveStage(audited, prefix+"-approve")
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

func seedAdaptationRevisionProject(t *testing.T, stage domain.ManuscriptStage, granularity string, completed bool) (*storepkg.Store, domain.AdaptationPlan, domain.AdaptationPlan) {
	t.Helper()
	st := storepkg.NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	base, manifest := adaptationRevisionServiceFixture(granularity)
	if err := st.Adaptation.SaveSourceManifest(manifest); err != nil {
		t.Fatal(err)
	}
	for _, source := range manifest.Chapters {
		events := make([]domain.AdaptationEvent, 0, 1)
		for _, event := range base.SourceEvents {
			if event.SourceChapter == source.Chapter {
				events = append(events, event)
			}
		}
		if err := st.Adaptation.SaveSourceReport(domain.AdaptationSourceReport{Chapter: source.Chapter, Title: source.Title, SourceSHA256: source.SHA256, Summary: source.Title, KeyEvents: []string{source.Title}, SourceEvents: events}); err != nil {
			t.Fatal(err)
		}
	}
	fullBase := adaptationRevisionTestClone(t, base)
	var basePtr *domain.AdaptationPlan
	var err error
	switch stage {
	case domain.ManuscriptStageProposalComplete:
		review := domain.AdaptationVolumeReview{Status: domain.AdaptationPlanStatusVolumeReview, Brief: base.Brief, SourceChapterCount: manifest.ChapterCount, Granularity: base.Granularity, RewritePolicy: base.RewritePolicy, WordTolerance: base.WordTolerance, TargetChapterCount: len(base.Chapters), MainlineRules: base.MainlineRules, RelationshipGoals: base.RelationshipGoals, Volumes: base.Volumes}
		if err := st.Adaptation.SaveVolumeReview(review); err != nil {
			t.Fatal(err)
		}
		basePtr, err = adaptationPlanFromVolumeReview(review, manifest, []domain.AdaptationSourceReport{
			{Chapter: 1, SourceEvents: []domain.AdaptationEvent{base.SourceEvents[0]}},
			{Chapter: 2, SourceEvents: []domain.AdaptationEvent{base.SourceEvents[1]}},
		})
	case domain.ManuscriptStageOutlineComplete:
		if err := st.Adaptation.SaveProposal(base); err != nil {
			t.Fatal(err)
		}
		basePtr, err = st.Adaptation.LoadProposal()
	default:
		if err := st.Adaptation.SavePlan(base); err != nil {
			t.Fatal(err)
		}
		basePtr, err = st.Adaptation.LoadPlan()
	}
	if err != nil || basePtr == nil {
		t.Fatalf("load stage adaptation contract: plan=%+v err=%v", basePtr, err)
	}
	base = *basePtr
	progress := &domain.Progress{NovelName: "adaptation", Phase: domain.PhaseInit, TotalChapters: 2, ChapterWordCounts: map[int]int{}}
	switch stage {
	case domain.ManuscriptStageProposalComplete:
		progress.Phase = domain.PhaseOutline
		if _, err := st.Adaptation.SetPlanningWorkflowStage(domain.AdaptationPlanningStageVolumeReviewPending, -1); err != nil {
			t.Fatal(err)
		}
	case domain.ManuscriptStageOutlineComplete:
		progress.Phase = domain.PhaseOutline
	case domain.ManuscriptStageWriting:
		progress.Phase, progress.Flow = domain.PhaseWriting, domain.FlowWriting
	case domain.ManuscriptStageComplete:
		progress.Phase, progress.Flow = domain.PhaseComplete, domain.FlowWriting
	}
	if completed {
		progress.CompletedChapters = []int{1}
		progress.ChapterWordCounts[1] = 100
		if err := st.Drafts.SaveFinalChapter(1, "completed body"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Progress.Save(progress); err != nil {
		t.Fatal(err)
	}
	candidate := adaptationRevisionTestClone(t, fullBase)
	candidate.TargetEventLedger = append(candidate.TargetEventLedger, domain.AdaptationEvent{ID: "added-event", Description: "original bridge", Origin: domain.AdaptationEventOriginAdded, Importance: domain.AdaptationEventSupporting, Required: true, DependsOn: []string{"source-event-2"}})
	candidate.Chapters = append(candidate.Chapters, domain.AdaptationChapterPlan{OutlineEntry: domain.OutlineEntry{ID: adaptationTestAddedID, Chapter: 3, Title: "Bridge", CoreEvent: "open the next phase", Hook: "new threat", Scenes: []string{"aftermath", "new conflict"}}, Chapter: 3, Title: "Bridge", IsAdded: true, AddedEventIDs: []string{"added-event"}, CoverageNote: "new story does not replace source coverage", TargetRunes: 3500, TargetMinRunes: 2500, TargetMaxRunes: 4500, RequiredChanges: []string{"add bridge"}, ForbiddenMoves: []string{"preserve source ending"}})
	candidate.Volumes[0].TargetTo = 3
	candidate.TargetTotalRunes += 3500
	candidate.TargetMaxRunes += 4500
	return st, base, candidate
}

func adaptationRevisionServiceFixture(granularity string) (domain.AdaptationPlan, domain.AdaptationSourceManifest) {
	chapters := []domain.AdaptationChapterPlan{
		{OutlineEntry: domain.OutlineEntry{ID: adaptationTestChapter1, Chapter: 1, Title: "One", CoreEvent: "meeting", Hook: "clue", Scenes: []string{"meet"}}, Chapter: 1, Title: "One", SourceChapters: []int{1}, SourceRange: domain.SourceRange{From: 1, To: 1}, SourceRunes: 1000, EventIDs: []string{"source-event-1"}, CoverageNote: "persisted source chapter one", TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000, PreserveEvents: []string{"meeting"}, RequiredChanges: []string{"adapt"}, ForbiddenMoves: []string{"do not drop meeting"}},
		{OutlineEntry: domain.OutlineEntry{ID: adaptationTestChapter2, Chapter: 2, Title: "Two", CoreEvent: "answer", Hook: "ending", Scenes: []string{"answer"}}, Chapter: 2, Title: "Two", SourceChapters: []int{2}, SourceRange: domain.SourceRange{From: 2, To: 2}, SourceRunes: 1000, EventIDs: []string{"source-event-2"}, DependsOnEventIDs: []string{"source-event-1"}, CoverageNote: "persisted source chapter two", TargetRunes: 4000, TargetMinRunes: 3000, TargetMaxRunes: 5000, PreserveEvents: []string{"answer"}, RequiredChanges: []string{"adapt"}, ForbiddenMoves: []string{"do not drop answer"}},
	}
	if granularity == domain.AdaptationGranularityChapter {
		chapters[0].SourceSegments = []domain.AdaptationSourceSegment{{SourceChapter: 1, Sequence: 1, EventIDs: []string{"source-event-1"}, RuneShare: domain.AdaptationSourceRuneShare{Start: 0, End: 1000}, EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{}}}
		chapters[1].SourceSegments = []domain.AdaptationSourceSegment{{SourceChapter: 2, Sequence: 1, EventIDs: []string{"source-event-2"}, RuneShare: domain.AdaptationSourceRuneShare{Start: 0, End: 1000}, EntryState: domain.AdaptationSegmentState{}, ExitState: domain.AdaptationSegmentState{}}}
	}
	plan := domain.AdaptationPlan{Granularity: granularity, ModePolicy: domain.AdaptationModePolicyForGranularity(granularity), Status: domain.AdaptationPlanStatusConfirmed, RewritePolicy: domain.AdaptationRewritePolicyForGranularity(granularity), Brief: "preserve source", WordTolerance: 0.15, SourceTotalRunes: 2000, TargetTotalRunes: 8000, TargetMinRunes: 6000, TargetMaxRunes: 10000, SourceEvents: []domain.AdaptationEvent{{ID: "source-event-1", Description: "meeting", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventMainline, SourceChapter: 1, Required: true}, {ID: "source-event-2", Description: "answer", Origin: domain.AdaptationEventOriginSource, Importance: domain.AdaptationEventSupporting, SourceChapter: 2, Required: true, DependsOn: []string{"source-event-1"}}}, Volumes: []domain.AdaptationVolumePlan{{ID: adaptationTestVolumeID, Index: 1, Title: "Source", TargetFrom: 1, TargetTo: 2, SourceFrom: 1, SourceTo: 2, MainlineEventIDs: []string{"source-event-1"}}}, Chapters: chapters}
	plan.Rules = domain.CompileAdaptationRules(plan.Brief, granularity)
	for index := range plan.Chapters {
		plan.Chapters[index].RuleIDs = domain.AdaptationRuleIDs(domain.ApplicableAdaptationRules(plan.Rules, granularity, plan.Chapters[index].Chapter))
	}
	manifest := domain.AdaptationSourceManifest{ChapterCount: 2, Chapters: []domain.AdaptationSource{{Chapter: 1, Title: "One", SHA256: "one", Runes: 1000}, {Chapter: 2, Title: "Two", SHA256: "two", Runes: 1000}}}
	return plan, manifest
}

func adaptationRevisionTestClone(t *testing.T, plan domain.AdaptationPlan) domain.AdaptationPlan {
	t.Helper()
	payload, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var clone domain.AdaptationPlan
	if err := json.Unmarshal(payload, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
