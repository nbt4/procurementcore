# ProcurementCore im Cores Designsystem

Die vollständigen und verbindlichen UI-Regeln liegen im Umbrella-Repository unter [`docs/DESIGN_SYSTEM.md`](https://github.com/nbt4/cores/blob/main/docs/DESIGN_SYSTEM.md). Dieses Dokument beschreibt ausschließlich die fachliche Ausprägung von ProcurementCore.

## Fachliche Dashboard-Struktur

ProcurementCore verwendet den gemeinsamen Dashboard-Vertrag in dieser Ausprägung:

1. zeitabhängige persönliche Begrüßung und Beschreibung der Einkaufslage;
2. vier Kennzahlen: offene Freigaben, ausgelöste Tiefpreise, Bestellvolumen und realisierte Einsparung;
3. „Jetzt bearbeiten“ für Freigaben und Preisalarme sowie ein Schnellstart für Bedarf, Katalog, Lieferanten und Bestellungen;
4. der fünfstufige Beschaffungsablauf;
5. letzte Einkaufsaktivitäten.

## Zulässige fachliche Besonderheiten

- Geldwerte und technische Referenzen dürfen JetBrains Mono verwenden.
- Erfolg, Warnung, Fehler und Information verwenden ausschließlich die suite-weiten semantischen Farben.
- Katalogparameter und Einkaufsstatus dürfen kompakt dargestellt werden, verändern aber weder Typografie-Leiter noch Tabellen- oder Formularstruktur.
- Die frühere Petrol-/Aptos-Palette und besonders kleinen 2–9-px-Radien sind außer Kraft.

## Wareneingang

Der Wareneingangsdialog zeigt vor der Buchung die Warehouse-Zuordnung und die
Auswirkung der Eingangsmenge. Bei Mengenführung werden alter und neuer Bestand,
bei Einzelverfolgung die Zahl der neu entstehenden und anschließend vorhandenen
Devices genannt. Fehlt die Zuordnung, bleibt die Buchungsaktion deaktiviert;
der Dialog bietet erkannte Warehouse-Produkte zum Verknüpfen sowie die
vorausgefüllte Neuanlage an. Lade-, Fehler- und Leerezustand bleiben im Dialog
sichtbar und die Verknüpfung kann nach einer externen Neuanlage erneut geprüft
werden. Auf schmalen Viewports stehen Auswahl und Aktionen untereinander.

## Adam-Hall-Warenkorb

Nach der Umwandlung eines freigegebenen Bedarfs bei einem Adam-Hall-Lieferanten
öffnet ProcurementCore den Live-Warenkorb automatisch. Bei bestehenden
Bestellungsentwürfen ist dieselbe Aktion im Bestellungsdialog erreichbar. Der
Dialog hält während der Serveranfrage seine Struktur, zeigt Fehler mit einer
Wiederholungsaktion und nennt Geschäftskonto, Lieferadresse, Versand- und
Zahlungsart sowie jede Position und den aktuellen Gesamtpreis. Die verbindliche
Bestellaktion bleibt bis zur ausdrücklichen Kontrollbestätigung deaktiviert.
Zugangsdaten und Shopware-Kontext werden nie an den Browser ausgegeben.

## PDF-Bestellimport

Die Bestellungsseite bietet neben der roten Primäraktion für Direktbestellungen
eine sekundäre PDF-Importaktion. Der erste Dialog enthält Dateiauswahl,
Größen-/Seitenlimit sowie einen stabilen Lade- und Fehlerzustand. Nach der
Analyse folgt das bestehende breite Bestellformular mit allen editierbaren
Feldern. Erkennungsgrad, Quelldatei, Summenabweichung und einzelne Prüfhinweise
stehen vor den Formularfeldern und verwenden ausschließlich die semantischen
Suite-Zustände. Die Anlage bleibt eine ausdrückliche Primäraktion; ein Upload
allein erzeugt noch keine Bestellung. Mobile Formulare und Positionszeilen
brechen gemäß den bestehenden Regeln einspaltig beziehungsweise zweispaltig um.

## Bedarf aus Angebots-PDF

Auf der Bedarfsseite öffnet die sekundäre Aktion „Aus Angebots-PDF“ zuerst einen
PDF-Upload und danach eine breite, vollständig bearbeitbare Vorschau. Text-PDFs
werden direkt gelesen; gescannte PDFs durchlaufen lokale OCR. Lieferant,
Angebotsnummer, Titel, Kostenstelle, Termine, Mengen, Preise und Katalogzuordnung
bleiben vor dem Speichern prüfbar. JEV kann nur nicht eindeutig zugeordnete
Positionen anhand begrenzter Katalogkandidaten vorschlagen; die Person bestätigt
die Auswahl. Unbekannte Positionen bleiben wahlweise Freitext oder werden über
eine ausdrücklich aktivierte Neuanlage mit SKU, Artikelname und Hersteller
zusammen mit dem Bedarf gespeichert. Die Anlage des Bedarfs, neuer Artikel und
ihrer Bezugsquellen erfolgt in einer Transaktion. Die PDF wird nicht dauerhaft
gespeichert. Fehlende Positionen werden nie aus einem Gesamtbetrag erfunden;
stattdessen können Positionen im Prüfschritt ergänzt werden. Prüfhinweise und
Fehler stehen direkt am Formular und sind ohne Farberkennung lesbar.

## Implementierung

`web/src/cores-theme.css` und `web/src/lib/cores-design.ts` sind generierte Dateien. Änderungen erfolgen in den kanonischen Quellen des Umbrella-Repositories und werden dort synchronisiert und geprüft. Lokale Komponenten verwenden die `suite-*`-Primitives und dürfen sie nur fachlich ergänzen.

## Adam-Hall-Bestätigung

Der Dialog verwendet ausschließlich bestehende Modal-/Formular-/Button- und
`suite-table-wrap`-Primitives. Öffnen lädt die lokale Vorschau ohne Lieferanten-
Schreibzugriff. Der erste Schritt zeigt Artikelnummern/Mengen und eine eigene
Warenkorb-Bestätigung; der zweite zeigt vollständige Lieferantenpositionen,
EUR-Summe, Geschäfts-Liefer-/Rechnungsadresse und Zahlungs-/Versandart mit einer
gesonderten verbindlichen Bestellbestätigung. Geänderte oder abgelaufene
Vorschauen blockieren den Versand. Fehler, ausstehende Ergebnisse und
Verbindungswiederholung behalten eine lesbare Meldung; kein automatischer
Neuaufbau oder Versand. Deutsch/Englisch und Geldformat folgen der gemeinsamen
Suite-Sprachwahl. Fokus bleibt im Dialog, Escape/Schließen sind während einer
Übermittlung gesperrt, Checkboxen sind beschriftet und Tabellen mobil scrollbar.

Hilfetexte und Tabellen verwenden die vorhandenen Suite-Schrift-/Abstands- und
Sekundärtext-Tokens für ausreichenden Kontrast. Originale Lieferantenbezeichnungen
und Positionen sind von der allgemeinen UI-Übersetzung ausgenommen; Sprachwahl
übersetzt ausschließlich Bedienelemente und formatiert Geldwerte.

Abnahme-Screenshots: [Warenkorb mobil](screenshots/adam-hall-cart-de-light-390.png),
[Bestellprüfung mobil](screenshots/adam-hall-paid-de-light-390.png),
[Warenkorb Desktop](screenshots/adam-hall-cart-en-dark-1280.png),
[Bestellprüfung Desktop](screenshots/adam-hall-paid-en-dark-1280.png).
Die Aufnahmen enthalten ausschließlich isolierte Testdaten.
