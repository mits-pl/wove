---
name: online-order
description: >
  Search and order products online. Compares prices across Polish marketplaces
  (Allegro, Ceneo, Google Shopping), presents options in a comparison table, and
  assists with checkout. Handles the full flow: priority selection, research,
  comparison, cart, and checkout handoff.
  Triggers on: "order", "buy", "find product", "compare prices", "cheapest",
  "zamow", "kup", "znajdz produkt", "porownaj ceny", "najtaniej".
user-invokable: true
argument-hint: "[product description]"
---

# Online Order: Product Research & Purchase Assistant

Search, compare, and order products across Polish online marketplaces.

---

## Web Interaction Rules (MANDATORY — applies to ALL phases)

These rules override any other instinct. Follow them for EVERY web interaction:

1. **ALWAYS use `web_capture` before clicking or typing.** Never guess CSS selectors. `web_capture` returns a screenshot with numbered element markers AND their exact CSS selectors. Use ONLY those selectors.
2. **Never guess selectors.** If `web_click` fails, do NOT try another guessed selector. Instead call `web_capture` again to get the current page state and correct selectors.
3. **Verify state after every action.** After adding to cart, use `web_capture` or `web_read_text` to confirm:
   - Did the cart count increase?
   - Did a confirmation popup appear?
   - Am I on the expected page?
4. **Never repeat a failed action.** If a click/selector fails twice, STOP and rethink. Use `web_capture` to see the current state.
5. **One add-to-cart click only.** After clicking "add to cart", verify the cart count. If it shows 1, you're done. Do NOT click again.
6. **Maximum 3 retries per step.** If you fail 3 times at any step (e.g., finding a button, loading a page), inform the user and ask for guidance.
7. **CSS selectors only.** Never use Playwright-style selectors like `text=...`. Only standard CSS selectors work with `web_click` and `web_type_input`.

---

## Phase 1 — Clarify Requirements (MANDATORY — DO NOT SKIP)

**STOP. Before doing ANYTHING else, you MUST complete this phase.**

1. If the product description ($ARGUMENTS) is vague, ask the user for specifics:
   - Brand, model, version
   - Size, color, capacity
   - Any other relevant attributes
2. Ask the user about their **priority**:
   - **Fast delivery** — prioritize shortest delivery time
   - **Lowest price** — prioritize cheapest option
   - **Compromise** — best value (balance of price and delivery speed)
3. Store the priority for decision-making in Phase 4.

**Wait for the user's response before proceeding to Phase 2.**

## Phase 2 — Load Owner Profile

1. Call `get_owner_profile` to read the owner's personal data.
2. If the profile is missing, **create it yourself**:
   - Ask the user for their details one by one:
     - Full name
     - Email address
     - Phone number
     - Delivery address (street, city, postal code)
     - Preferred payment method (BLIK / card / transfer)
     - Any delivery notes (e.g., preferred parcel locker, delivery hours)
   - Once you have all the data, use `write_text_file` to create the owner profile file at the path returned by `get_owner_profile`. Use this format:
     ```
     # Owner Profile

     - **Name**: [name]
     - **Email**: [email]
     - **Phone**: [phone]
     - **Address**: [address]
     - **Payment**: [payment method]
     - **Notes**: [delivery notes]
     ```
   - Confirm to the user that the profile has been saved.
3. Note the owner's payment preference and delivery notes for later use.

## Phase 3 — Research Products

Search across multiple Polish marketplaces using `run_sub_task` for each source.
**You MUST use `run_sub_task` for each marketplace — do NOT search manually in the main conversation.**

### Sources to search (in priority order):
1. **Allegro.pl** — largest Polish marketplace
2. **Ceneo.pl** — price comparison aggregator
3. **Google Shopping** (google.pl → Shopping tab)

### Sub-task instructions for each marketplace:

```
Search [marketplace] for: [product description]

Steps:
1. Open [marketplace URL] with web_open
2. Use web_capture to see the page and find the search box
3. Type the product query using web_type_input with the selector from web_capture
4. Press Enter with web_press_key to submit search
5. Use web_capture to see search results
6. Extract the TOP 5 results with these details:
   - Product name (full, with brand/model)
   - Price (in PLN, including shipping if visible)
   - Delivery time (e.g., "tomorrow", "2-3 days", "5-7 days")
   - Seller rating (if available)
   - Product URL
7. Save results as markdown to /tmp/order-research/[marketplace].md
8. Close the browser widget with close_widget

IMPORTANT: Use web_capture BEFORE every click or type action to get correct CSS selectors.
Extract ONLY the key data listed above. Do NOT save full page text.
Format each result as a numbered list with the fields on separate lines.
```

### Marketplace-specific URLs:
- Allegro: `https://allegro.pl`
- Ceneo: `https://www.ceneo.pl`
- Google Shopping: `https://www.google.pl/search?tbm=shop&q=[encoded query]`

## Phase 4 — Compare & Recommend

1. Read all sub-task output files from `/tmp/order-research/`.
2. Present a **markdown comparison table** to the user:

```
| # | Product | Shop | Price (PLN) | Delivery | Rating | Notes |
|---|---------|------|-------------|----------|--------|-------|
| 1 | ...     | ...  | ...         | ...      | ...    | ...   |
| 2 | ...     | ...  | ...         | ...      | ...    | ...   |
```

3. **Recommend the best option** based on the user's stated priority:
   - **Fast delivery**: pick the option with shortest delivery time (break ties by price)
   - **Lowest price**: pick the cheapest option (break ties by delivery time)
   - **Compromise**: balance price and delivery — e.g., if option A is 10 PLN cheaper but takes 4 days longer, option B might be better value
4. Explain your reasoning briefly (e.g., "Option 2 is 15 PLN cheaper but takes 3 days longer than option 1").
5. Ask the user to **confirm your recommendation or pick a different option**.
6. Wait for user confirmation before proceeding.

## Phase 5 — Purchase

1. Open the chosen product URL with `web_open`.
2. **Use `web_capture` to see the product page** and identify the add-to-cart button.
3. **Add to cart**:
   - Click the add-to-cart button using the CSS selector from `web_capture`
   - **Verify**: Use `web_capture` to confirm the item was added (check cart count or confirmation message)
   - If cart count did NOT change, try once more. If still fails, inform the user.
4. **Navigate to checkout**:
   - Use `web_capture` to find the checkout/cart link
   - Click through to the checkout/order form
5. **Fill in owner details** from the profile:
   - Use `web_capture` at each form step to identify input fields
   - Email address
   - Full name (first name + last name)
   - Phone number
   - Delivery address (street, city, postal code)
6. **Select payment method** matching the owner's preference from profile (e.g., BLIK, card).
7. **Apply delivery preferences** from the owner's Notes field if applicable (e.g., select InPost parcel locker if noted).

## Phase 6 — Handoff to User

1. **STOP before the final payment/order confirmation button.**
2. Tell the user clearly:
   > I've added **[product name]** to cart at **[shop name]** for **[price] PLN** and filled in your details.
   > Please review the order in browser widget **[widget_id]** and complete payment yourself.
   > I will NOT click the payment button — this is for your safety.
3. **Do NOT close** the checkout browser widget — the user needs it.
4. **Clean up**:
   - Close any remaining research browser widgets with `close_widget`
   - Delete research files: use `term_run_command` to run `rm -rf /tmp/order-research/`

---

## Safety Rules (CRITICAL)

- **NEVER** click "Pay", "Confirm order", "Zaplac", "Zamawiam i place", "Potwierdz zamowienie", or any equivalent payment/confirmation button.
- **NEVER** enter credit card numbers, CVV codes, BLIK codes, or banking credentials.
- **NEVER** complete a payment transaction on behalf of the user.
- If a **CAPTCHA** appears, tell the user to solve it manually in the browser widget, then continue.
- If **login is required**, tell the user to log in first in the browser widget, then continue.
- If a marketplace **blocks automated browsing**, inform the user and try the next marketplace.
- If **no results found** on any marketplace, inform the user and suggest alternative search terms.
