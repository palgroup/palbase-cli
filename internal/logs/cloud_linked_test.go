package logs

import (
	"testing"
)

// BULUT PROJESİNE LİNKLİ BİR CHECKOUT DA LOGLARINI OKUYABİLMELİ.
//
// Aynı proje SEÇİMLE zaten okunabiliyordu; yalnız LİNKLİ bir checkout
// reddediliyordu — yani cevap, linkin varlığına göre değişiyordu (ölçüldü
// 25.08.2026: linkliyken "does not run on this machine", seçimliyken satırlar).
// Reddin dayandığı ölçüm KİRACININ yönetim yüzeyi içindi ve hâlâ doğru; ama
// logları sunan taraf kontrol düzlemi.
func TestACloudAddressResolvesToItsRef(t *testing.T) {
	r := Resolvers{CloudRef: func(url string) (string, bool) {
		if url == "https://8bbwb2pbm.palbase.studio" {
			return "8bbwb2pbm", true
		}
		return "", false
	}}

	ref, ok := cloudRefOf(r, "https://8bbwb2pbm.palbase.studio")
	if !ok || ref != "8bbwb2pbm" {
		t.Fatalf("bulut adresi çözülmedi: %q %v", ref, ok)
	}

	// SELF-HOST BİR ADRES BULUT SAYILMAZ: onun logları kendi konteynerlerinde
	// ve orada bir düzlem yok.
	if _, ok := cloudRefOf(r, "https://stack.ornek.com"); ok {
		t.Fatal("self-host adres bulut sayıldı")
	}
}

// ÇÖZÜCÜ YOKSA "HAYIR" — bir bağımlılığın yokluğu, bir adresi bulut projesi
// SAYMAK için gerekçe değildir.
func TestNoResolverMeansNotCloud(t *testing.T) {
	if _, ok := cloudRefOf(Resolvers{}, "https://8bbwb2pbm.palbase.studio"); ok {
		t.Fatal("çözücü yokken bulut sayıldı")
	}
}
