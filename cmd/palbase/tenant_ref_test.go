package main

import "testing"

// A HOST IS CASE-INSENSITIVE (FR-002). `https://k3xq81w4m.PALBASE.STUDIO` names the
// same environment as its lowercase spelling; compared case-sensitively it fell
// to the address path, where a project key could commit the retired
// `{"url": …}` record.
func TestATenantAddressIsReadInAnyLetterCase(t *testing.T) {
	for _, tc := range []struct {
		url     string
		wantRef string
		wantOK  bool
	}{
		{"https://k3xq81w4m.palbase.studio", "k3xq81w4m", true},
		{"https://K3XQ81W4M.palbase.studio", "k3xq81w4m", true},
		{"https://k3xq81w4m.PALBASE.STUDIO", "k3xq81w4m", true},
		{"https://k3xq81w4m.Palbase.Studio/", "k3xq81w4m", true},
		{"https://api.example.com", "", false},
		{"https://a.b.palbase.studio", "", false},
	} {
		ref, ok := tenantRefOf(tc.url, "palbase.studio")
		if ref != tc.wantRef || ok != tc.wantOK {
			t.Errorf("tenantRefOf(%q) = %q, %v; want %q, %v", tc.url, ref, ok, tc.wantRef, tc.wantOK)
		}
	}
}
