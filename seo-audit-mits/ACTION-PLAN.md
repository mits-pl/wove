# Plan działań SEO dla mits.pl (priorytetyzowany)

## Założenia
- Cel: zwiększenie liczby i jakości leadów B2B z kanału organicznego (PL + rynki zagraniczne).  
- Horyzont: **0–3 mies. (quick wins)**, **3–6 mies. (rozbudowa)**, **6–12 mies. (skalowanie)**.  
- Odpowiedzialność: marketing + dev + właściciele ofert/usług.

---

## 1. Priorytety 0–3 miesiące (quick wins, MUST DO)

### 1.1. Meta title, H1 i podstawowe on-page na kluczowych landingach
**Cel:** zwiększenie CTR i dopasowania do intencji wyszukiwania.

1. Wybrać 5–10 najważniejszych stron ofertowych (np. outsourcing, utrzymanie, dedykowane oprogramowanie, kontakt).  
2. Dla każdej:
   - napisać nowy **meta title** z główną frazą transakcyjną + benefit (max 60–65 znaków),  
   - doprecyzować **H1** (fraza + jasna obietnica),  
   - uzupełnić meta description pod CTR (problem → rozwiązanie → CTA),  
   - dodać w treści 1–2 naturalne wystąpienia fraz lokalnych tam, gdzie to ma sens (Wrocław/Polska).

**Efekt:** szybka poprawa widoczności na frazy komercyjne + lepszy CTR z obecnych pozycji.

---

### 1.2. Rozbudowa treści ofertowych (problem → rozwiązanie → proof)
**Cel:** podniesienie konwersji i jakości leadów + wzmocnienie E‑E‑A‑T.

Dla każdego kluczowego landingu:
1. Dodać sekcje:
   - "Dla kogo jest ta usługa?" (typy firm, branże, wielkość, stack),  
   - "Jak pracujemy?" (proces, etapy, narzędzia, modele współpracy),  
   - "Jakie rezultaty dowozimy?" (konkretne efekty + linki do case’ów),  
   - "Najczęstsze obawy i jak je adresujemy" (czas, budżet, komunikacja, bezpieczeństwo).
2. Upewnić się, że na każdej stronie jest wyraźne **CTA** (formularz, kontakt, konsultacja).

**Efekt:** więcej wartościowych zapytań, lepsze sygnały dla Google (czas na stronie, zaangażowanie).

---

### 1.3. Sekcje FAQ + schema FAQPage
**Cel:** przechwycenie długiego ogona zapytań i przygotowanie pod AI Overviews.

1. Na 5–10 landingach utworzyć sekcje FAQ (5–8 realnych pytań klientów).  
2. Odpowiedzi pisać w formie zwięzłych, eksperckich mini-poradników.  
3. Wdrożyć schema `FAQPage` (JSON-LD) dla tych sekcji.

**Efekt:** lepsza widoczność na pytania, większa szansa na wyróżnione wyniki/AI Overviews.

---

### 1.4. Szybkie poprawki Core Web Vitals
**Cel:** poprawa doświadczenia użytkownika i sygnałów rankingowych.

1. Zidentyfikować 3–5 najwolniejszych URL-i (homepage + kluczowe landingi).  
2. Dla tych stron:
   - skompresować największe obrazy,  
   - przeformatować je do WebP/AVIF,  
   - włączyć lazy-load dla obrazów poniżej "folda",  
   - przejrzeć skrypty trzecie (tag manager, chaty, widgety) – co da się przeładować później, przenieść.

**Efekt:** lepsze LCP/CLS/INP na priorytetowych wejściach.

---

### 1.5. Schemat linkowania wewnętrznego (MVP)
**Cel:** wzmocnienie kluczowych landingów i poprawa indeksacji.

1. Dla każdej głównej usługi wybrać:
   - min. 3 powiązane case studies,  
   - min. 3 powiązane artykuły/poradniki.
2. W treściach:
   - dodać sekcje typu "Przykładowe realizacje" i "Polecane artykuły" z linkami,  
   - zadbać o opisowe anchory (nie tylko "czytaj więcej").
3. Z artykułów i case’ów dodać link z powrotem do odpowiedniej usługi.

**Efekt:** mocniejsze sygnały tematyczne, łatwiejsza nawigacja i lepsza indeksacja podstron.

---

### 1.6. Dane strukturalne – pierwszy etap
**Cel:** czytelne sygnały dla Google, przygotowanie pod AI/LLM.

1. Zweryfikować i uzupełnić `Organization` (NAP, social, kontakt).  
2. Dla strony kontaktowej/lokalnej wdrożyć `LocalBusiness`/`ProfessionalService`.  
3. Przygotować szablon `Service` (JSON-LD) i wdrożyć na 3–5 kluczowych landingach.  
4. Dla kilku artykułów eksperckich wdrożyć `Article`/`BlogPosting` z autorami.

**Efekt:** bogatsze dane dla wyszukiwarki, większa szansa na lepszą interpretację treści.

---

## 2. Priorytety 3–6 miesięcy (rozbudowa)

### 2.1. Architektura informacji i klastry tematyczne
**Cel:** skalowalna podstruktura pod SEO i AI Overviews.

1. Zmapować główne obszary usługowe (np. rozwój oprogramowania, utrzymanie, integracje, konsulting).  
2. Dla każdego obszaru:
   - zbudować strukturę: **landing usługi → podstrony branżowe/use case → treści eksperckie**,  
   - zadbać o logiczne nawigacje (menu, breadcrumbs, linki w stopce).
3. Wybrać 2–3 klastry na start i wdrożyć je kompleksowo.

**Efekt:** większa widoczność na długi ogon, łatwiejsze skalowanie treści.

---

### 2.2. Program contentowy (blog/poradniki/case studies)
**Cel:** generowanie ruchu edukacyjno-decyzyjnego.

1. Na bazie rozmów sprzedażowych i zapytań klientów przygotować listę 30–50 tematów treści.  
2. Podzielić je na:  
   - TOP of funnel (edukacja, problemy biznesowe),  
   - MID (porównania rozwiązań, checklisty),  
   - BOT (case’y, konkretne scenariusze wdrożeniowe).
3. Ustalić rytm publikacji (np. 2–4 treści miesięcznie) i zawsze powiązywać je z odpowiednimi landingami usług.

**Efekt:** stabilny napływ ruchu, lepsze wsparcie procesu decyzyjnego klienta.

---

### 2.3. Rozwinięcie danych strukturalnych i FAQ
**Cel:** pełniejsze opisanie usług i treści dla wyszukiwarki.

1. Rozszerzyć wdrożenie `Service` i `FAQPage` na kolejne landingi.  
2. Dla kluczowych artykułów dodać sekcje FAQ (pytania z wyszukiwań/klientów) + schema.  
3. Spiąć dane strukturalne z contentem (frazy, nazwy usług, autorzy) w spójny sposób.

**Efekt:** lepsza interpretacja tematyki przez Google + dodatkowe punkty pod AI.

---

### 2.4. Wzmocnienie E‑E‑A‑T i sygnałów zaufania
**Cel:** poprawa konwersji i postrzegania marki.

1. Rozbudować sekcje "O nas", "Zespół", "Jak pracujemy".  
2. Dodać/usystematyzować:
   - profile autorów treści (bio, doświadczenie, specjalizacja),  
   - studia przypadków z konkretnymi wynikami (liczby, wykresy),  
   - logotypy klientów z krótkimi opisami projektów.

**Efekt:** silniejsza pozycja ekspercka, więcej sygnałów jakości dla Google i użytkowników.

---

## 3. Priorytety 6–12 miesięcy (skalowanie)

### 3.1. Skalowanie klastrów tematycznych
**Cel:** dominacja w wybranych niszach/obszarach problemowych.

1. Dla najlepiej rokujących klastrów (na bazie danych z 3–6 mies.):
   - rozwijać kolejne podstrony use case/branż,  
   - tworzyć uzupełniające treści (webinary, e-booki, landing page’e kampanijne).
2. Budować dodatkowe mosty w linkowaniu wewnętrznym.

**Efekt:** silne pozycje na szeroki zakres zapytań wokół kluczowych usług.

---

### 3.2. Optymalizacja ciągła UX/CWV i CRO
**Cel:** maksymalne wykorzystanie ruchu organicznego.

1. Regularne pomiary CWV (PageSpeed, Search Console) i praca iteracyjna nad:  
   - LCP/CLS/INP,  
   - wagą stron,  
   - JS/CSS,  
   - kolejnością ładowania zasobów.
2. Testy A/B:  
   - warianty CTA,  
   - formularze,  
   - sekcje hero,  
   - długość treści.

**Efekt:** rosnący współczynnik konwersji z istniejącego ruchu.

---

### 3.3. Wzmocnienie SEO lokalnego i obecności w zewnętrznych serwisach
**Cel:** lepsza widoczność na rynku polskim (szczególnie Wrocław/region).

1. Dopracowanie i aktywne prowadzenie Google Business Profile.  
2. Praca nad recenzjami/ocenami w serwisach branżowych (Clutch itp.).  
3. Spójne wykorzystanie fraz lokalnych w profilach i na stronie.

**Efekt:** więcej zapytań lokalnych i wzmocnienie brandu.

---

## 4. KPI i monitoring

1. **Ruch organiczny:** wzrost sesji z organic (PL + zagranica).  
2. **Lead generation:** liczba i jakość leadów z kanału organicznego (zapytania, demo, konsultacje).  
3. **Widoczność:** liczba fraz w top3/top10 dla głównych usług i klastrów tematycznych.  
4. **Core Web Vitals:** odsetek URL-i w zielonej strefie w Search Console.  
5. **Zaangażowanie:** czas na stronie, liczba odsłon na sesję, współczynnik odrzuceń na kluczowych landingach.

Regularny przegląd co 4–6 tygodni pozwoli korygować plan i przesuwać zasoby tam, gdzie widać największy zwrot.