package main

import (
	"testing"

	"github.com/voocel/ainovel-cli/internal/entry/web"
)

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

func TestParseCLIOptionsWebDefaults(t *testing.T) {
	opts, args, err := parseCLIOptions([]string{"web"})
	if err != nil {
		t.Fatalf("parseCLIOptions web: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("web should not leave positional args: %v", args)
	}
	if !opts.Web || opts.WebHost != web.DefaultHost || opts.WebPort != web.DefaultPort {
		t.Fatalf("web defaults mismatch: %+v", opts)
	}
}

func TestParseCLIOptionsWebFlags(t *testing.T) {
	opts, args, err := parseCLIOptions([]string{
		"--config", "config.json",
		"web",
		"--host", "0.0.0.0",
		"--port", "9900",
		"--runtime-root", "D:/novels",
		"--open",
	})
	if err != nil {
		t.Fatalf("parseCLIOptions web flags: %v", err)
	}
	if len(args) != 0 {
		t.Fatalf("unexpected args: %v", args)
	}
	if !opts.Web || opts.WebHost != "0.0.0.0" || opts.WebPort != 9900 || opts.WebRuntimeRoot != "D:/novels" || !opts.WebOpen {
		t.Fatalf("web options mismatch: %+v", opts)
	}
	if opts.ConfigPath != "config.json" {
		t.Fatalf("config path = %q", opts.ConfigPath)
	}
}

func TestParseCLIOptionsRejectsWebMixedWithHeadless(t *testing.T) {
	if _, _, err := parseCLIOptions([]string{"--headless", "web"}); err == nil {
		t.Fatal("expected web mixed with headless to fail")
	}
}
