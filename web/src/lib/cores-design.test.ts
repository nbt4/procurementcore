// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from 'vitest';
import {
  initSuiteI18n,
  pairSuiteTranslations,
  setSuiteLanguage,
  suiteLocale,
  suiteTranslate,
} from './cores-design';
import de from './cores-locales/de.json';
import en from './cores-locales/en.json';

describe('suite i18n', () => {
  beforeEach(() => {
    localStorage.clear();
    document.body.innerHTML = '';
  });

  it('pairs stable locale keys and switches language suite-wide', () => {
    const translations = pairSuiteTranslations(
      { common: { save: 'Speichern' } },
      { common: { save: 'Save' } },
    );
    initSuiteI18n(translations);

    setSuiteLanguage('en');
    expect(localStorage.getItem('cores_language')).toBe('en');
    expect(document.documentElement.lang).toBe('en');
    expect(suiteTranslate('Speichern')).toBe('Save');
    expect(suiteLocale()).toBe('en-GB');

    setSuiteLanguage('de');
    expect(suiteTranslate('Speichern')).toBe('Speichern');
    expect(suiteTranslate('Save')).toBe('Speichern');
    expect(suiteLocale()).toBe('de-DE');
  });

  it('translates interpolated values in either source language', () => {
    initSuiteI18n(pairSuiteTranslations(
      { status: { online: '{{healthy}} von {{total}} Komponenten online' } },
      { status: { online: '{{healthy}} of {{total}} components online' } },
    ));

    setSuiteLanguage('en');
    expect(suiteTranslate('6 von 8 Komponenten online')).toBe('6 of 8 components online');

    setSuiteLanguage('de');
    expect(suiteTranslate('6 of 8 components online')).toBe('6 von 8 Komponenten online');
  });

  it('translates existing UI text and restores the German source', () => {
    document.body.innerHTML = '<button title="Speichern"> Speichern </button>';
    initSuiteI18n(pairSuiteTranslations(
      { common: { save: 'Speichern' } },
      { common: { save: 'Save' } },
    ));

    setSuiteLanguage('en');
    const button = document.querySelector('button')!;
    expect(button.textContent).toBe(' Save ');
    expect(button.title).toBe('Save');

    setSuiteLanguage('de');
    expect(button.textContent).toBe(' Speichern ');
    expect(button.title).toBe('Speichern');
  });

  it('covers mixed-source navigation and every Core dashboard', () => {
    initSuiteI18n(pairSuiteTranslations(de, en));

    setSuiteLanguage('en');
    expect(suiteTranslate('Was heute zählt – über alle Cores hinweg.')).toBe('What matters today across all Cores.');
    expect(suiteTranslate('Heute läuft kein terminierter Job. Nutze die Übersicht für die nächsten Aufträge.')).toBe('No scheduled job is running today. Use the overview for upcoming jobs.');
    expect(suiteTranslate('Prioritäten, Materialfluss und Einsatzbereitschaft auf einen Blick.')).toBe('Priorities, equipment flow, and operational readiness at a glance.');
    expect(suiteTranslate('Deine Aufgaben, Termine und Pläne auf einen Blick.')).toBe('Your tasks, dates, and plans at a glance.');
    expect(suiteTranslate('Bedarfe, Bezugsquellen und Bestellungen auf einem Stand.')).toBe('Requisitions, sources, and orders in one place.');
    expect(suiteTranslate('6 von 8 Komponenten online')).toBe('6 of 8 components online');

    setSuiteLanguage('de');
    expect(suiteTranslate('Contacts')).toBe('Kontakte');
    expect(suiteTranslate('6 of 8 components online')).toBe('6 von 8 Komponenten online');
  });
});
