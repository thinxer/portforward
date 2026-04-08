package main

import (
	"reflect"
	"testing"
)

func TestParseProxyJumpSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		spec      string
		want      []jumpSpec
		wantError bool
	}{
		{
			name: "empty",
			spec: "",
			want: nil,
		},
		{
			name: "none",
			spec: "none",
			want: nil,
		},
		{
			name: "comma separated",
			spec: "bastion,user@edge:2200,ssh://ops@relay:2222",
			want: []jumpSpec{
				{ref: "bastion"},
				{ref: "edge", user: "user", port: "2200"},
				{ref: "relay", user: "ops", port: "2222"},
			},
		},
		{
			name:      "none with others",
			spec:      "none,bastion",
			wantError: true,
		},
		{
			name:      "invalid port",
			spec:      "bastion:abc",
			wantError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseProxyJumpSpec(tt.spec)
			if tt.wantError {
				if err == nil {
					t.Fatalf("parseProxyJumpSpec(%q) succeeded, want error", tt.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseProxyJumpSpec(%q) error = %v", tt.spec, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseProxyJumpSpec(%q) = %#v, want %#v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestParseJumpEndpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		want      jumpSpec
		wantError bool
	}{
		{
			name:  "plain alias",
			input: "bastion",
			want:  jumpSpec{ref: "bastion"},
		},
		{
			name:  "user and port",
			input: "alice@bastion:2200",
			want:  jumpSpec{ref: "bastion", user: "alice", port: "2200"},
		},
		{
			name:  "bracketed ipv6",
			input: "[2001:db8::1]:2022",
			want:  jumpSpec{ref: "2001:db8::1", port: "2022"},
		},
		{
			name:      "unbracketed ipv6",
			input:     "2001:db8::1",
			wantError: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseJumpEndpoint(tt.input)
			if tt.wantError {
				if err == nil {
					t.Fatalf("parseJumpEndpoint(%q) succeeded, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJumpEndpoint(%q) error = %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseJumpEndpoint(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}
