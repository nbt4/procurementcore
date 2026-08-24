# ProcurementCore Designsystem

## Leitidee

ProcurementCore ist ein Arbeitsinstrument für Einkauf und Freigaben, kein Marketing-Dashboard. Die visuelle Richtung heißt **Einkaufsakte**: dunkel, präzise, informationsdicht und zurückhaltend. Jede Hervorhebung muss eine fachliche Funktion haben.

## Visuelle Regeln

- Graphit und tiefes Petrolschwarz bilden Navigation, Arbeitsfläche und Panels.
- Das gemeinsame RentalCore-Rot ist der einzige Markenakzent. Es kennzeichnet aktive Navigation, Primäraktionen und wenige Orientierungspunkte.
- Erfolgs-, Warn-, Fehler- und Informationsfarben bleiben semantisch und werden nicht dekorativ verwendet.
- Radien bleiben zwischen 2 und 9 Pixeln. Statusmarker sind keine Pills.
- Panels verwenden Haarlinien statt Leuchteffekten oder großen Schatten. Schatten sind Dialogen und Toasts vorbehalten.
- Keine dekorativen Verläufe, Glows, Glassmorphism-Flächen, schwebenden Kreise oder Hero-Bereiche innerhalb der Anwendung.
- Kennzahlen werden als zusammenhängende Übersicht mit tabellarischen Ziffern dargestellt, nicht als Sammlung austauschbarer Einzelkarten.
- Navigation, Tabellen und Formulare haben Vorrang vor Illustrationen. Bilder werden nur verwendet, wenn sie konkrete Produktinformation vermitteln.
- Texte sind direkt und fachlich. Englische Eyebrow-Zeilen und dekorative Claims werden vermieden.
- Fokus-, Hover-, Aktiv- und Disabled-Zustände müssen erkennbar sein; reduzierte Bewegung wird respektiert.

## Recherchebasis

- [Coupa Spend Analysis](https://www.coupa.com/products/procure-to-pay/spend-analysis/) für filterbare, kennzahlenorientierte Einkaufsansichten
- [Ivalua Source-to-Pay](https://www.ivalua.com/solutions/process/source-to-pay-platform/) für dichte Lieferanten- und Prozessübersichten
- [Carbon Data Table](https://carbondesignsystem.com/components/data-table/usage/) für robuste Enterprise-Tabellenmuster
- [GOV.UK Colour](https://design-system.service.gov.uk/styles/colour/) und [Type Scale](https://design-system.service.gov.uk/styles/type-scale/) für funktionale Farben, Kontrast und konsistenten vertikalen Rhythmus
- [No Slop UI](https://github.com/LeoStehlik/no-slop-ui) als zusätzliche Review-Checkliste gegen generische Agenten-UI-Muster

## Kerntokens

| Rolle | Wert |
| --- | --- |
| Arbeitsfläche | `#0d1214` |
| Panel | `#12191b` |
| Eingabefläche | `#182123` |
| Primärtext | `#edf0ed` |
| Sekundärtext | `#b7bfbc` |
| Akzent | `#d0021b` |
| Akzent Hover | `#a00115` |
| Akzent hell | `#f87171` |
| Rahmen | `#2a3537` |

Die implementierten Tokens liegen in `web/src/theme.css`; Komponenten- und Responsive-Regeln liegen in `web/src/index.css`.
