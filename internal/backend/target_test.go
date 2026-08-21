package backend

import "testing"

// BİR HEDEFİN BU MAKİNEDE OLUP OLMADIĞI, ADRESİNDEN OKUNUR.
//
// `Target.URL`'in yorumu "set for a project running on this machine" diyordu ve
// bu, `link` yalnız yerel bir yığını gösterebilirken doğruydu. Artık
// `palbase link https://<ref>.v2.palbase.studio` UZAK bir projeyi yazıyor.
//
// Ayrım olmadan `palbase logs` her linkli hedefte yerel konteyner arıyor ve
// bulut projesinde "No such container: palbase-todoapp-runtime-1" diyor —
// hiç var olmayacak bir konteynerin adını vererek (canlıda ölçüldü 2026-08-21).
func TestATargetKnowsWhetherItIsOnThisMachine(t *testing.T) {
	local := []string{
		"http://localhost:54321",
		"https://127.0.0.1",
		"http://[::1]:8080",
		"https://127.0.0.1:8443/",
	}
	for _, u := range local {
		if !(Target{URL: u}).OnThisMachine() {
			t.Fatalf("%q bu makinede sayılmalı", u)
		}
	}

	remote := []string{
		"https://juvuev3mm.v2.palbase.studio",
		"https://app.dev.palbase.studio",
		// Adı "localhost" İÇEREN ama bu makine OLMAYAN bir adres: alt dizge
		// eşlemesi burada yanlış cevap verirdi.
		"https://localhost.example.com",
	}
	for _, u := range remote {
		if (Target{URL: u}).OnThisMachine() {
			t.Fatalf("%q bu makinede SAYILMAMALI", u)
		}
	}
}
