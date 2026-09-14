package backend

import (
	"encoding/json"
	"testing"
)

// StackNames'in TELE çıkan şekli, üreticinin BEKLEDİĞİ şekil olmalı. İkisi iki
// repoda yaşıyor (Go tarafı burada, `stack-gen.ts` @palbase/backend'de), yani
// aralarında derleyici yok — bu test o boşluğu tutuyor.
//
// Üreticinin sözleşmesi (stack-gen.ts BucketInput): kova ya düz bir ad, ya da
// {name, variants}. Variant'sız kova BOŞ dizi taşır ve üretici onu `never`
// olarak render eder.
func TestStackNamesMarshalsInTheGeneratorsShape(t *testing.T) {
	raw, err := json.Marshal(StackNames{
		Secrets: []string{"STRIPE_KEY"},
		Flags:   []string{"new_ui"},
		Buckets: []StackBucket{
			{Name: "posts", Variants: []string{"card", "thumb"}},
			{Name: "docs", Variants: []string{}},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got struct {
		Buckets []struct {
			Name     string   `json:"name"`
			Variants []string `json:"variants"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v — %s", err, raw)
	}
	if len(got.Buckets) != 2 {
		t.Fatalf("iki kova bekleniyordu: %s", raw)
	}
	if got.Buckets[0].Name != "posts" || len(got.Buckets[0].Variants) != 2 {
		t.Errorf("variant'lar tele çıkmadı: %s", raw)
	}
	// NEGATİF KONTROL: variant'sız kova `null` DEĞİL boş dizi göndermeli —
	// üretici boş birliği `never` yapar, null ise "sorulmadı" demektir.
	if got.Buckets[1].Variants == nil {
		t.Errorf("variant'sız kova null gönderdi, boş dizi göndermeliydi: %s", raw)
	}
}
