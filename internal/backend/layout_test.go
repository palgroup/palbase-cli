package backend

import (
	"strings"
	"testing"
)

// DÜZEN TEK YERDEN OKUNUR. İki yerde yazılan bir yol bir gün ayrışır ve yazan
// ile okuyan farklı dosyaya bakar — bu deponun kendi geçmişinde ölçülmüş bir
// kusur sınıfı (ignore listesi iki rename boyunca kimsenin yazmadığı bir adı
// taşıdı, `build`'in GERÇEKTEN yarattığı dizini ise kimse ignore etmedi).
func TestLayoutPaths(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{RootDir(), "palbase"},
		{EnvDir("main"), "palbase/environments/main"},
		{SpecPath("main"), "palbase/environments/main/openapi.json"},
		{RolesPath("local"), "palbase/environments/local/roles.json"},
		{ConfigPath("main", "ios"), "palbase/environments/main/ios-config.json"},
		{ConfigPath("main", "macos"), "palbase/environments/main/macos-config.json"},
		{PlistPath("main"), "palbase/environments/main/Palbase-Info.plist"},
		{ConfigPath("main", "android"), "palbase/environments/main/android-config.json"},
		{ConfigPath("main", "web"), "palbase/environments/main/web-config.json"},
		{GeneratedPath("main", "ios"), "palbase/environments/main/PalbaseGenerated.swift"},
		{GeneratedPath("main", "macos"), "palbase/environments/main/PalbaseGenerated.swift"},
		{GeneratedPath("main", "web"), "palbase/environments/main/palbe.gen.ts"},
		{ClientBarrelPath(), "palbase/client.ts"},
	} {
		if tc.got != tc.want {
			t.Errorf("yol yanlış: %q, beklenen %q", tc.got, tc.want)
		}
	}
}

// ANDROID'İN CHECKOUT'TA ÜRETİLEN DOSYASI YOKTUR — eklenti build dizinine üretir.
// Boş olmayan bir yol döndürmek, CLI'a orada bir dosya arattırırdı.
func TestAndroidHasNoGeneratedPathInTheCheckout(t *testing.T) {
	if got := GeneratedPath("main", "android"); got != "" {
		t.Errorf("android için üretilen yol %q — eklenti build dizinine üretiyor, checkout'ta yolu yok", got)
	}
}

// ORTAM DİZİNİ DÜZ: platform bir ALT DİZİN değil, DOSYA ADI.
//
// Ölçülen Xcode dışlama deseni (`*/palbase/environments/*/*`) tam olarak bu
// seviye sayısına bağlı — Xcode'un bu ayarlarında `*` dizin sınırını GEÇMİYOR
// (gerçek build ile ölçüldü). Bir alt dizin eklemek deseni sessizce etkisiz
// kılar: hiçbir şey dışlanmaz, iki ortamın istemcisi birden derlenir.
func TestEnvironmentDirectoryIsFlat(t *testing.T) {
	for _, p := range []string{
		ConfigPath("main", "ios"), ConfigPath("main", "web"), ConfigPath("main", "android"),
		PlistPath("main"),
		GeneratedPath("main", "ios"), GeneratedPath("main", "web"),
		SpecPath("main"), RolesPath("main"),
	} {
		if got := strings.Count(p, "/"); got != 3 {
			t.Errorf("%s: %d ayraç — ortam dizini düz olmalı (palbase/environments/<env>/<dosya>)", p, got)
		}
	}
}

// ESKİ DÜZEN ADIYLA DEĞİL İÇERİĞİYLE ÖLÇÜLÜR (D-008).
//
// `Palbase` bu listede DEĞİL ve yokluğu dersin kendisi: macOS ve Windows'ta
// dosya sistemi büyük/küçük harf duyarsız, yani `palbase` ile `Palbase` TEK
// dizin. Adı ölçen bir kapı, yeni kökü taşıyan her checkout'u — yani ilk
// başarılı `link`ten sonra hepsini — reddederdi; silen bir yol ise müşterinin
// yeni dizinini silerdi. Gizli kök (`.palbase`) gerçek bir ikinci dizin, o
// kalıyor; görünür kökün emekliliği `LegacyMarkers` ile ölçülüyor.
func TestLegacyRootsAreNamed(t *testing.T) {
	got := LegacyRoots()
	if len(got) != 1 || got[0] != ".palbase" {
		t.Errorf("eski kökler: %v", got)
	}
	for _, name := range got {
		if strings.EqualFold(name, RootDir()) {
			t.Errorf("%q, bu CLI'ın kendi kökünden yalnız BÜYÜK/KÜÇÜK HARFLE ayrılıyor — "+
				"duyarsız bir dosya sisteminde aynı dizin", name)
		}
	}
	if len(LegacyMarkers()) == 0 {
		t.Error("görünür kökün emekliliğini ölçecek hiçbir işaret yok")
	}
}

// YOL AYRACI HER ZAMAN `/`: bu yollar git'e, `.gitattributes`e ve Xcode'un
// dışlama desenine giriyor. `filepath.Join` Windows'ta `\` yazar ve desen
// eşleşmez — orada da müşterinin uygulaması iki istemciyle derlenir.
func TestSeparatorIsAlwaysForwardSlash(t *testing.T) {
	for _, p := range []string{EnvDir("main"), SpecPath("main"), ConfigPath("main", "ios"), ClientBarrelPath()} {
		if strings.Contains(p, "\\") {
			t.Errorf("%q ters bölü taşıyor — bu yollar git ve Xcode deseni için", p)
		}
	}
}
