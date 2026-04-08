package main

import "testing"

func TestParseArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		args      []string
		want      options
		wantError bool
	}{
		{
			name: "gateway only",
			args: []string{"prod"},
			want: options{gateway: "prod"},
		},
		{
			name: "proxy jump override",
			args: []string{"-J", "bastion,edge", "prod"},
			want: options{gateway: "prod", proxyJump: "bastion,edge"},
		},
		{
			name:      "missing gateway",
			args:      []string{"-J", "bastion"},
			wantError: true,
		},
		{
			name:      "too many args",
			args:      []string{"prod", "extra"},
			wantError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseArgs(tt.args)
			if tt.wantError {
				if err == nil {
					t.Fatalf("parseArgs(%q) succeeded, want error", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseArgs(%q) error = %v", tt.args, err)
			}
			if got != tt.want {
				t.Fatalf("parseArgs(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}
