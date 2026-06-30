package main

import "testing"

func TestParseCLIOptionsAdaptFlags(t *testing.T) {
	opts, args, err := parseCLIOptions([]string{
		"--headless",
		"--adapt", "source.txt",
		"--adapt-granularity", "arc",
		"--adapt-rewrite-policy", "preserve_details",
		"--adapt-word-tolerance", "0.2",
		"--prompt", "改编 brief",
	})
	if err != nil {
		t.Fatalf("parseCLIOptions: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("unexpected args: %v", args)
	}
	if opts.AdaptPath != "source.txt" || opts.AdaptGranularity != "arc" || opts.AdaptRewritePolicy != "preserve_details" || opts.AdaptWordTolerance != 0.2 {
		t.Fatalf("adapt options mismatch: %+v", opts)
	}
}

func TestParseCLIOptionsRejectsAdaptFlagsWithoutAdapt(t *testing.T) {
	if _, _, err := parseCLIOptions([]string{
		"--adapt-rewrite-policy", "preserve_details",
		"--prompt", "改编 brief",
	}); err == nil {
		t.Fatal("expected adapt rewrite policy without --adapt to fail")
	}
}
