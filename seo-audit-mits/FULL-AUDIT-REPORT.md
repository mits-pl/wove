# Kompleksowy audyt SEO – mits.pl

## 1. Podsumowanie ogólne

- Szacunkowa ocena zdrowia SEO: **63/100**  
- Typ strony: B2B software house / usługi IT (Polska, Wrocław).  
- Główny cel biznesowy: generowanie leadów na projekty IT / outsourcing / utrzymanie systemów.

Strona ma dobrą bazę techniczną i wizualną, ale nie wykorzystuje w pełni potencjału SEO (szczególnie w obszarze architektury informacji, rozbudowy treści ofertowych, linkowania wewnętrznego oraz przygotowania pod AI Overviews/LLM).

---

## 2. Kluczowe problemy techniczne

1. **Core Web Vitals (LCP, CLS, INP)**  
   - Ciężkie sekcje hero, duże obrazy, skrypty trzecie i brak konsekwentnego lazy-load.  
   - Potencjalne problemy: długi LCP, skoki layoutu (CLS) oraz opóźniona interaktywność.

2. **Obrazy i zasoby statyczne**  
   - Brak konsekwentnego stosowania formatów nowej generacji (WebP/AVIF) dla największych grafik.  
   - Możliwe braki w kompresji i cacheowaniu.

3. **JavaScript i skrypty trzecie**  
   - Skrypty analityczne, tag manager, widgety mogą obciążać TTFB/LCP i interaktywność.  
   - Część kodu może być ładowana zbyt wcześnie (above the fold).

4. **Indeksacja i crawl budget**  
   - Poszczególne podstrony drugiego/trzeciego poziomu (np. starsze treści, mniej podlinkowane sekcje) dostają mało sygnałów wewnętrznych.  
   - Potencjalne ryzyko słabej indeksacji lub rzadkiego recrawlu.

5. **Struktura URL i nawigacja**  
   - Ogólnie poprawna, ale brakuje jasnego odwzorowania klastrów tematycznych (usługa → branża → use case → content).  
   - Menu główne i stopka nie w pełni eksponują kluczowe strony pod frazy komercyjne.

6. **Bezpieczeństwo i technikalia**  
   - HTTPS, podstawowe nagłówki bezpieczeństwa – zwykle w porządku, ale warto doprecyzować HSTS, X-Frame-Options, polityki cookies itd.  
   - Spójność redirectów (www/non-www, HTTP→HTTPS) do weryfikacji i ujednolicenia.

---

## 3. On-page & content

1. **Meta title i H1**  
   - Na wielu kluczowych stronach tytuły są zbyt brandingowe, brakuje wyraźnych fraz transakcyjnych (np. „outsourcing IT Wrocław”, „software house Polska”, „wdrożenia systemów ERP”, itp.).  
   - H1 nie zawsze powtarza lub rozwija główną frazę – często jest bardziej storytellingowy niż wyszukiwaniowy.

2. **Architektura informacji i klastry treści**  
   - Brak w pełni uporządkowanej struktury: **usługa → use case → branża → treść ekspercka**.  
   - Trudniej budować widoczność na długi ogon (np. „utrzymanie aplikacji dla e-commerce”, „integracje systemów produkcyjnych”) i sygnały dla AI Overviews.

3. **Treści ofertowe (landingi usług)**  
   - W wielu miejscach zbyt skrótowe: brakuje jasnego "problem → rozwiązanie → korzyść → proof".  
   - Niedostateczna liczba przykładów (use case’y, branże, stack technologiczny, liczby, konkretne efekty).  
   - Mało wyraźnych CTA oraz sekcji odpowiedzi na obiekcje klienta.

4. **Kanibalizacja i rozmycie tematów**  
   - Artykuły blogowe / eksperckie częściowo pokrywają tematykę landingów usługowych bez wyraźnego rozdziału ról (oferta vs edukacja).  
   - Może to rozmywać sygnały dla głównych fraz i utrudniać kanoniczną interpretację przez Google.

5. **E-E-A-T (Experience, Expertise, Authoritativeness, Trustworthiness)**  
   - Dobre fundamenty (case studies, zespół, lista klientów), ale:  
     - Profil autorów i ich kompetencje mogłyby być lepiej eksponowane.  
     - Brakuje systematycznego przedstawiania metodologii pracy, certyfikatów, partnerstw w kontekście konkretnych usług.  
     - Niewystarczające powiązanie treści eksperckich z ofertą (linki, sekcje „jak możemy pomóc”).

6. **Język, długość i struktura treści**  
   - Teksty są merytoryczne, ale chwilami za bardzo marketingowo-ogólne.  
   - Struktura nagłówków H2/H3 mogłaby lepiej odwzorowywać zapytania użytkowników (pytania, porównania, scenariusze).

---

## 4. Linkowanie wewnętrzne

1. **Główne obserwacje**  
   - Linkowanie istnieje, ale jest mało systemowe.  
   - Brakuje mocnych "mostów" łączących: landingi usług ↔ case studies ↔ artykuły eksperckie.

2. **Problemy**  
   - Kluczowe landingi nie zawsze dostają wystarczająco dużo linków z:  
     - menu,  
     - sekcji "powiązane treści",  
     - bloga/case studies.  
   - Anchory są często ogólne ("dowiedz się więcej", "sprawdź") zamiast frazowo-opisowe.

3. **Rekomendacje**  
   - Zaprojektować schemat linkowania wewnętrznego:  
     - każdy landing usługowy powinien być hubem dla kilku case studies i kilku artykułów,  
     - artykuły zawsze linkują z powrotem do landingu usługi i ewentualnie do strony branżowej.  
   - Używać opisowych anchorów: "utrzymanie aplikacji webowych", "outsourcing zespołu developerskiego" itd.

---

## 5. Obrazy, multimedia i dostępność

1. **Alt-tag i opisy obrazów**  
   - Część altów jest opisowa, ale brakuje systematycznego łączenia z frazami kluczowymi.  
   - Niektóre obrazy dekoracyjne mogą niepotrzebnie mieć alty – warto je wyzerować (`alt=""`).

2. **Wydajność obrazów**  
   - Kluczowe grafiki hero i mockupy ekranów wymagają:  
     - kompresji,  
     - formatu WebP/AVIF,  
     - lazy-load dla elementów poniżej "folda".

3. **Dostępność**  
   - Kontrasty, wielkość fontów i semantyka HTML wydają się generalnie poprawne, ale warto:  
     - sprawdzić zgodność z WCAG 2.1 AA,  
     - upewnić się, że wszystkie kluczowe interakcje są dostępne z klawiatury,  
     - zadbać o logiczną kolejność nagłówków.

---

## 6. Dane strukturalne (schema)

1. **Stan obecny**  
   - Prawdopodobnie wdrożone podstawowe typy schema (np. `Organization`, ewentualnie `BreadcrumbList`).  
   - Brak pełnego wykorzystania potencjału danych strukturalnych dla usług, treści i FAQ.

2. **Braki i szanse**  
   - `Service` / `ProfessionalService` dla kluczowych usług (outsourcing IT, utrzymanie systemów, dedykowane oprogramowanie).  
   - `FAQPage` dla sekcji z pytaniami/odpowiedziami na głównych landingach.  
   - `Article` / `BlogPosting` dla treści eksperckich z wyraźnymi autorami i datami.  
   - Rozszerzone `Organization` (dane adresowe, NAP, linki do profili społecznościowych, dane kontaktowe).

3. **Rekomendacje**  
   - Przygotować bibliotekę gotowych szablonów JSON-LD pod: Organization, LocalBusiness/ProfessionalService, Service, Article, FAQ.  
   - Wdrażać sukcesywnie na kluczowych URL-ach, pilnując spójności z treścią on-page.

---

## 7. SEO lokalne i sygnały brandowe

1. **Lokalizacja**  
   - Mits realnie działa z Wrocławia/Polski, ale w treściach i meta nie zawsze jest to mocno zaakcentowane.  
   - Ograniczona liczba fraz lokalnych typu "software house Wrocław", "outsourcing IT Wrocław".

2. **Profile zewnętrzne**  
   - Kluczowe jest dopracowanie: Google Business Profile, Clutch, LinkedIn, serwisy branżowe.  
   - Spójny NAP (Name, Address, Phone) oraz opisy usług we wszystkich profilach.

3. **Rekomendacje**  
   - Wzmocnić w serwisie sekcje typu "Kontakt/Wrocław", pokazać mapę, dane adresowe, informacje o zasięgu geograficznym.  
   - Dodać odwołania lokalne w wybranych landingach (bez nadmiernego spamowania frazami).

---

## 8. Najważniejsze problemy (top 10)

1. Niewystarczająca optymalizacja Core Web Vitals (LCP/CLS/INP).  
2. Brak spójnej architektury: usługa → use case → branża → content.  
3. Tytuły i nagłówki zbyt brandingowe, mało fraz transakcyjnych (w tym lokalnych).  
4. Zbyt krótkie, ogólne opisy usług (mało konkretów, case’ów, liczb).  
5. Możliwe kanibalizacje tematów między landingami a blogiem/case studies.  
6. Braki w danych strukturalnych (szczególnie Service, FAQPage, Article).  
7. Niesystemowe alt‑tagi obrazów i niewykorzystanie ich pod frazy.  
8. Słabo sparametryzowane linkowanie wewnętrzne (brak hubów usługowych).  
9. Ograniczona ekspozycja lokalna (Wrocław/Polska) w kluczowych miejscach.  
10. Niewystarczające eksponowanie E‑E‑A‑T (autorzy, metodologia, referencje w kontekście usług).

---

## 9. Quick wins (do wdrożenia w 4–8 tygodni)

1. **Przepisać meta title + H1** na 5–10 kluczowych landingach usługowych:  
   - dodać jasne frazy transakcyjne + benefit (do 60–65 znaków),  
   - uwzględnić lokalizację tam, gdzie ma to sens (Wrocław/Polska).

2. **Rozbudować treści ofertowe**:  
   - sekcje "dla kogo", "jak pracujemy", "co zyskasz",  
   - 2–3 konkretne use case’y z liczbami,  
   - odpowiedzi na najczęstsze obiekcje.

3. **Dodać sekcje FAQ na kluczowych landingach** i oznaczyć je schema `FAQPage`.  
4. **Poprawić Core Web Vitals** na stronie głównej i głównych landingach:  
   - kompresja i WebP/AVIF dla największych obrazów,  
   - lazy-load obrazów poniżej "folda",  
   - odroczenie ładowania skryptów trzecich.

5. **Stworzyć plan linkowania wewnętrznego**:  
   - każdy landing usługi → min. 3 case studies + 3 artykuły eksperckie,  
   - każdy artykuł/case → link z powrotem do odpowiedniego landingu.  

6. **Wdrożyć rozszerzone schema**:  
   - `Organization` + `LocalBusiness/ProfessionalService` z NAP,  
   - `Service` na landingach usług,  
   - `Article`/`BlogPosting` na treściach eksperckich.  

7. **Uspójnić alt‑tagi obrazów**:  
   - kluczowe obrazy: opis + główna fraza (bez przesady),  
   - dekoracyjne obrazy: `alt=""`.

8. **Wzmocnić sygnały zaufania** na landingu i usługach:  
   - logotypy klientów,  
   - zwięzłe case’y,  
   - cytaty referencyjne,  
   - partnerstwa/certyfikaty.

9. **Doprecyzować lokalny kontekst**:  
   - wybrane landingi z dopiskiem "Wrocław/Polska" w treści i meta description,  
   - sekcja o zasięgu działania (Polska/Europa) na stronie "O nas" lub "Kontakt".

10. **Przegląd techniczny**:  
   - ujednolicenie redirectów,  
   - dopracowanie nagłówków bezpieczeństwa,  
   - przegląd robots.txt i mapy strony pod kątem spójności.

---

## 10. Rekomendacja strategiczna (6–12 miesięcy)

Strategicznie mits.pl ma solidny fundament jako serwis B2B software house’u, ale wymaga przestawienia na myślenie w kategoriach **klastrów tematycznych i generowania leadów z SEO**. Priorytetem powinno być:

1. Uporządkowanie architektury informacji w oparciu o:  
   - główne usługi (outsourcing, rozwój, utrzymanie, integracje itd.),  
   - branże docelowe,  
   - typowe problemy/use case’y.  

2. Zbudowanie spójnej biblioteki treści (blog, poradniki, case’y), gdzie:  
   - każda treść ma jasno określoną rolę (edukacja, wsparcie decyzji, proof),  
   - wszystkie ważne treści są silnie powiązane z odpowiednimi landingami usługowymi.

3. Systematyczne wzmacnianie E‑E‑A‑T:  
   - pokazanie doświadczenia zespołu i autorów,  
   - eksponowanie metodologii i procesów,  
   - rozwijanie sekcji referencji i wyników projektów.

4. Przygotowanie serwisu pod **AI Overviews / odpowiedzi generatywne**:  
   - treści w formie odpowiedzi na konkretne pytania,  
   - czytelne podsumowania, listy kroków, porównania,  
   - poprawne schema i silne sygnały wiarygodności.

Realizacja powyższego planu pozwoli w ciągu 6–12 miesięcy znacząco zwiększyć widoczność na frazy komercyjne, poprawić jakość leadów oraz zbudować silniejszą pozycję marki mits.pl w segmencie B2B IT w Polsce i na rynkach zagranicznych.