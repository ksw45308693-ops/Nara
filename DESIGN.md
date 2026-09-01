# Design system

## Direction

Operate-mode interface derived from the supplied concept image. The dashboard reproduces the four-stage input, processing, organization, and output flow. Functional pages extend the same visual language without decorative effects or nested card layouts.

## Tokens

- Navy `#042B63`: titles, active navigation, primary actions, dark process cards.
- Cyan `#00A7E8`: rules, outlines, focus, and links.
- Pale blue `#EEF8FC`: secondary panels and future-feature cards.
- Light gray `#F0F1F3`: neutral process cards and table headers.
- Green `#197A35`: healthy automation state and success feedback.
- Ink `#17243A`, muted `#627084`, white `#FFFFFF`.
- Korean UI font stack: `Pretendard`, `Noto Sans KR`, `Apple SD Gothic Neo`, `Malgun Gothic`, sans-serif.
- Radius: 10px for panels and controls; pills only for small status labels.
- Motion: 150–200ms for state changes only; content is visible by default.

## Layout

- Desktop shell: 232px side navigation and a fluid content area.
- Dashboard first viewport: title, concise explanation, four connected process cards, automation status, and two future-feature panels.
- Mobile: navigation collapses; process cards stack vertically; tables become labeled rows.
- Body copy stays within 75 characters where it is prose. Data tables may be wider.

## Components and states

Buttons, links, inputs, select controls, navigation, tables, alerts, process cards, and status labels must include default, hover, focus, active, disabled, loading, error, and empty behavior where applicable. Use semantic HTML before JavaScript.

## Copy

Use short Korean action labels. Preserve factual terms from 나라장터. Do not invent customer claims or performance numbers. Mark demonstration data as sample data.
