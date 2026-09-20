// @vitest-environment jsdom

import { beforeEach, describe, expect, it } from 'vitest';
import {
  initSuiteI18n,
  pairSuiteTranslations,
  setSuiteLanguage,
  suiteLocale,
  suiteTranslate,
} from './cores-design';

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
    expect(suiteLocale()).toBe('de-DE');
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
});
