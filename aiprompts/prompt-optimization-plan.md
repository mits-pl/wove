# Optymalizacja promptów systemowych Wove dla Gemini 2.5 Pro

## Stan obecny - co działa dobrze

1. **Modularny system kontekstu** - dynamiczne ładowanie `project_stack`, `project_rules`, `project_structure`, planów i skilli
2. **Sekcjonowanie instrukcji w WAVE.md** - parsowanie na sekcje z tagami technologii i filtrowanie po rozszerzeniu pliku
3. **Lazy loading kontekstu** - `project_instructions` jako tool call zamiast wrzucania wszystkiego do system promptu
4. **Sub-taski** - izolacja kontekstu dla dużych zadań
5. **XML tags** (`<project_stack>`, `<project_rules>`, `<active_plan>`) - strukturyzacja kontekstu

## Problemy i rekomendacje

### 1. Prompt jest jednym długim paragrafem

`SystemPromptText_OpenAI` łączy wszystko `" "` (spacją) w jeden ciągły tekst. Gemini potrzebuje wyraźnie rozdzielonych sekcji. Badania pokazują, że XML tagi i nagłówki markdown w system instruction znacząco poprawiają adherencję do instrukcji.

**Fix:** Zmiana joina z `" "` na `"\n\n"` + dodanie nagłówków `## Sekcja` do każdego bloku.

### 2. Brak wariantu promptu dla Gemini

Aider odkrył, że Gemini wymaga innego formatu edycji (diff-fenced zamiast standardowego). Wove używa tego samego promptu dla wszystkich modeli.

**Fix:** Stworzenie `SystemPromptText_Gemini` z:
- Instrukcją "Be concise" (najskuteczniejsza kontrola gadatliwości dla Gemini)
- Jawnym chain-of-thought: "Think step-by-step before taking action, but keep your reasoning brief"
- Silniejszym naciskiem na format wyjścia

### 3. Brak dyrektywy parallel tool calls

Cursor jawnie instruuje model: "maximize parallel tool calls". Wove tego nie robi.

**Fix:** Dodanie do promptu: "When multiple tool calls are independent (e.g., reading several files, running unrelated commands), execute them in parallel in a single response."

### 4. ExtractCriticalRules jest zbyt naiwne

Skanuje po keywordach "must", "always", "never" itp. - ale "must" pojawia się w zwykłych zdaniach. To może wciągać nierelewantne reguły.

**Fix:** Dodanie dedykowanej sekcji `## Critical Rules` w WAVE.md i priorytetowe parsowanie.

### 5. Project tree na depth 2 to za mało dla świadomości architekturalnej

Aider używa tree-sitter do wyciągania definicji klas/funkcji (repo map). Cursor embeduje cały codebase. Wove daje tylko nazwy plików na 2 poziomach głębokości.

**Fix:** Generowanie "repo map" - lista kluczowych plików z eksportowanymi symbolami (klasy, funkcje). Nawet prosty grep po `function `, `class `, `export` dałby lepszą mapę niż samo drzewo katalogów.

### 6. Trójpoziomowa architektura kontekstu

Najskuteczniejszy wzorzec z badań akademickich ("Codified Context" paper):

| Poziom | Co zawiera | Kiedy ładowany | Token budget |
|--------|-----------|----------------|--------------|
| **Hot (Constitution)** | Reguły kodu, konwencje nazewnicze, architektura, known failure modes | Zawsze | ~500-800 tokens |
| **Domain Specialists** | Instrukcje per-technologia (PHP, Vue, DB) | Per-task na podstawie plików | ~1000-2000 tokens |
| **Cold (Knowledge Base)** | Pełna dokumentacja, wzorce kodu, API docs | On-demand przez tool call | Nielimitowany |

Wove już ma elementy tego (project_stack = hot, sekcje WAVE.md = specialists, project_instructions tool = cold), ale granice są rozmyte. Formalizacja poprawi efektywność.

### 7. Stateful tool responses

Cursor dołącza do odpowiedzi narzędzi metadane kontekstowe (CWD, git branch, exit code). To pozwala modelowi podejmować lepsze decyzje bez dodatkowych tool calls.

**Fix:** Do odpowiedzi `term_run_command` i `read_text_file` dodawać krótki kontekst: `[cwd: /app/src, git: feature/auth, last_exit: 0]`

### 8. Plan-then-execute z review gates

Badania HumanLayer (35K linii Rust w 7h) pokazują, że review w 2 momentach jest kluczowy:
- **Po research** - żeby nie iść w złą stronę
- **Po planie** - "zła linia planu = setki złych linii kodu"

Wove ma `plan_create` ale nie wymusza review planu przed wykonaniem.

**Fix:** Dodać do promptu: "After creating a plan, STOP and present it to the user. Wait for approval before implementing."

### 9. Context window utilization monitoring

FIC (Frequent Intentional Compaction) zaleca utrzymywanie context window na 40-60%.

**Fix:** Wstrzykiwać do system promptu info o zużyciu kontekstu. Gdy >50%, dodać notę: "Context usage: 62%. Consider using run_sub_task for remaining steps."

### 10. Read-before-write enforcement

Claude Code ma twarde reguły: "Edit tool will FAIL if you did not read the file first." Wove mówi "read before you write" ale nie wymusza tego.

**Fix:** Walidacja w `ToolVerifyInput` dla `write_text_file` i `edit_text_file` - sprawdzenie czy plik był wcześniej czytany w sesji.

---

## Plan implementacji

### Quick wins (1-2h każdy)

- [x] **QW-1:** Zmiana joina z `" "` na `"\n\n"` + dodanie nagłówków sekcji markdown
- [x] **QW-2:** Dodanie dyrektywy parallel tool calls
- [x] **QW-3:** Dodanie "Be concise" i explicit thinking instructions dla Gemini

### Medium effort (pół dnia każdy)

- [x] **ME-0:** Wzmocnione wymuszenie czytania kodu przed pisaniem + nowa sekcja Architecture Matching
- [ ] **ME-1:** Stworzenie `SystemPromptText_Gemini` z wariantem promptu
- [x] **ME-2:** Stateful tool responses (CWD, git branch w odpowiedziach narzędzi)
- [x] **ME-3:** Context usage monitoring + ostrzeżenie o zapełnieniu

### Larger effort (1-2 dni każdy)

- [x] **LE-1:** Repo map via gotreesitter (pure Go tree-sitter, 8 languages)
- [ ] **LE-2:** Formalizacja trójpoziomowej architektury kontekstu
- [x] **LE-3:** Read-before-write enforcement w tool verification

---

## Źródła

- [Aider Edit Formats](https://aider.chat/docs/more/edit-formats.html) - model-specific prompt variants
- [Cursor Architecture Deep Dive](https://medium.com/@lakkannawalikar/cursor-ai-architecture-system-prompts-and-tools-deep-dive-77f44cb1c6b0)
- [Codified Context Paper](https://arxiv.org/html/2602.20478v1) - three-tier context architecture
- [Advanced Context Engineering (HumanLayer/FIC)](https://github.com/humanlayer/advanced-context-engineering-for-coding-agents/blob/main/ace-fca.md)
- [Gemini 2.5 Pro Best Practices](https://medium.com/google-cloud/best-practices-for-prompt-engineering-with-gemini-2-5-pro-755cb473de70)
- [Cline System Prompt](https://cline.bot/blog/system-prompt-advanced)
- [Roo Code Prompt Structure](https://docs.roocode.com/advanced-usage/prompt-structure)
- [Claude Code System Prompts](https://github.com/Piebald-AI/claude-code-system-prompts)
