# ProcurementCore Agent Rules

## Mandatory suite design contract

- Before any UI work, read `../docs/DESIGN_SYSTEM.md` and `../theme/README.md` in the `cores` umbrella, or the canonical documents in `github.com/nbt4/cores` for standalone work.
- `web/src/cores-theme.css` and `web/src/lib/cores-design.ts` are generated and must never be edited directly.
- The former standalone “Einkaufsakte” palette is retired. ProcurementCore uses the same Inter typography, grayscale surfaces, red accent, form/table/dropdown/scrollbar rules, 256/80 px sidebar and dashboard hierarchy as every Core.
- Dashboard greetings must use `suiteGreeting()` and prefer the shared profile display name.
- Run the umbrella design check, frontend tests/build and Go tests before release; keep `docs/DESIGN.md` and README synchronized.
