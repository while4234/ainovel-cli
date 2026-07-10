package store

import (
	"os"

	"github.com/voocel/ainovel-cli/internal/adaptaudit"
)

const (
	adaptationAuditReportFile       = adaptationRootDir + "/audits/latest.json"
	adaptationRepairApplicationFile = adaptationRootDir + "/audits/latest_application.json"
)

func (s *AdaptationStore) SaveAuditReport(report adaptaudit.Report) error {
	if err := adaptaudit.ValidateReportDigest(report); err != nil {
		return err
	}
	return s.io.WriteJSON(adaptationAuditReportFile, report)
}

func (s *AdaptationStore) LoadAuditReport() (*adaptaudit.Report, error) {
	var report adaptaudit.Report
	if err := s.io.ReadJSON(adaptationAuditReportFile, &report); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if err := adaptaudit.ValidateReportDigest(report); err != nil {
		return nil, err
	}
	return &report, nil
}

func (s *AdaptationStore) SaveRepairApplication(application adaptaudit.RepairApplication) error {
	return s.io.WriteJSON(adaptationRepairApplicationFile, application)
}

func (s *AdaptationStore) LoadRepairApplication() (*adaptaudit.RepairApplication, error) {
	var application adaptaudit.RepairApplication
	if err := s.io.ReadJSON(adaptationRepairApplicationFile, &application); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return &application, nil
}
