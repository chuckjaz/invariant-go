package main

import (
	"flag"
	"testing"
)

func TestMountFlagParsing(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		expectedRoot  string
		expectedMount string
	}{
		{
			name:          "positional root sets both root and mount",
			args:          []string{"my-slot"},
			expectedRoot:  "my-slot",
			expectedMount: "my-slot",
		},
		{
			name:          "positional root with explicit mount flag",
			args:          []string{"--mount", "/tmp/mnt", "my-slot"},
			expectedRoot:  "my-slot",
			expectedMount: "/tmp/mnt",
		},
		{
			name:          "explicit root flag with explicit mount flag",
			args:          []string{"--root", "hex123", "--mount", "/tmp/mnt"},
			expectedRoot:  "hex123",
			expectedMount: "/tmp/mnt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsFlags := flag.NewFlagSet("mount", flag.ContinueOnError)
			var mountpoint string
			fsFlags.StringVar(&mountpoint, "mount", "", "")
			var commonFlags CommonMountFlags
			commonFlags.Register(fsFlags)

			if err := fsFlags.Parse(tt.args); err != nil {
				t.Fatalf("Parse failed: %v", err)
			}

			if fsFlags.NArg() > 0 {
				rootParam := fsFlags.Arg(0)
				if commonFlags.RootAddr == "" && commonFlags.Slot == "" {
					commonFlags.RootAddr = rootParam
				}
				if mountpoint == "" {
					mountpoint = rootParam
				}
			}

			if commonFlags.RootAddr != tt.expectedRoot {
				t.Errorf("Expected RootAddr %q, got %q", tt.expectedRoot, commonFlags.RootAddr)
			}
			if mountpoint != tt.expectedMount {
				t.Errorf("Expected mountpoint %q, got %q", tt.expectedMount, mountpoint)
			}
		})
	}
}
