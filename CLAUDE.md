# Palbase CLI — Development Rules

palbase CLI (Go). Bu bir submodule'dür (palgroup/palbase-cli). Genel kurallar için root `CLAUDE.md`.
User-facing string'ler İngilizce (task Türkçe olsa bile).

## Agent Team Workflow

> **Paralel yazma = WORKTREE ZORUNLU.** Bu modüle eşzamanlı yazacak her agent kendi git worktree'sinde çalışır: `Agent(..., isolation: "worktree")`. Branch DEĞİL — branch tek working tree'yi paylaşır, agent'lar birbirini ezer (reset-war, half-landed kırık commit). Worktree izolasyon verince serialize/`blockedBy` GEREKMEZ; paralel yaz. Aynı dosyanın aynı yerine iki agent yazacaksa worktree de çözmez → task'ları üst üste binmeyen dosya setlerine BÖL. Merge'ü lead sıralı yapar; parent (palbase) submodule pointer bump'ı tek elden. Bump mekaniği + ayrıntı: root `CLAUDE.md` → "Paralel Yazma İzolasyonu" / "Submodule Pointer Bump".
