# Palbase CLI — Development Rules

palbase CLI (Go). Bu bir submodule'dür (palgroup/palbase-cli). Genel kurallar için root `CLAUDE.md`.
User-facing string'ler İngilizce (task Türkçe olsa bile).

## Agent Team Workflow

> **Worktree YOK, yan branch YOK — her şey `main`'de.** Bu repo'nun tek working tree'si ve tek branch'i vardır: `main`. `Agent(..., isolation: "worktree")`, `git worktree add`, `git checkout -b` YASAK. Aynı repo'ya aynı anda TEK yazıcı: iki agent yazacaksa sırayla koştur veya işi tek agent'a ver; paralellik istiyorsan farklı REPO'lara dağıt. Yarım kalan iş yan branch'te ATIL kalıp çürür — main'e commit'le, push'la. Parent submodule pointer bump'ı tek elden (lead). Gerekçe + ayrıntı: root `CLAUDE.md` → "WORKTREE YOK, YAN BRANCH YOK" / "Submodule Pointer Bump".
