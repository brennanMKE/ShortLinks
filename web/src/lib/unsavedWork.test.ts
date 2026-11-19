// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest';
import {
  hasDirtyField,
  hasOpenDialog,
  hasUnsavedInput,
  hasUnsavedWork,
  isEditingField,
  resetUnsavedForTests,
  setUnsaved,
} from './unsavedWork';

describe('unsavedWork', () => {
  afterEach(() => resetUnsavedForTests());

  it('setUnsaved marks and clears per key', () => {
    setUnsaved('a', true);
    expect(hasUnsavedWork()).toBe(true);
    setUnsaved('a', false);
    expect(hasUnsavedWork()).toBe(false);
  });

  it('hasUnsavedWork stays true while any key is unsaved', () => {
    setUnsaved('a', true);
    setUnsaved('b', true);
    setUnsaved('a', false);
    expect(hasUnsavedWork()).toBe(true);
    setUnsaved('b', false);
    expect(hasUnsavedWork()).toBe(false);
  });

  it('isEditingField is true for text inputs, textareas and contenteditable', () => {
    expect(isEditingField({ tagName: 'INPUT', type: 'text' })).toBe(true);
    expect(isEditingField({ tagName: 'INPUT', type: 'email' })).toBe(true);
    expect(isEditingField({ tagName: 'INPUT' })).toBe(true);
    expect(isEditingField({ tagName: 'TEXTAREA' })).toBe(true);
    expect(isEditingField({ tagName: 'DIV', isContentEditable: true })).toBe(true);
  });

  it('isEditingField is false for buttons, checkboxes, null and non-fields', () => {
    expect(isEditingField({ tagName: 'INPUT', type: 'checkbox' })).toBe(false);
    expect(isEditingField({ tagName: 'INPUT', type: 'submit' })).toBe(false);
    expect(isEditingField({ tagName: 'BUTTON' })).toBe(false);
    expect(isEditingField({ tagName: 'BODY' })).toBe(false);
    expect(isEditingField(null)).toBe(false);
  });

  describe('DOM scan (#0185)', () => {
    afterEach(() => {
      document.body.innerHTML = '';
    });

    it('is clean on an empty page', () => {
      expect(hasUnsavedInput()).toBe(false);
    });

    it('an open dialog (New link, batch grid) counts as unsaved work', () => {
      document.body.innerHTML = '<dialog open><form><input type="url" /></form></dialog>';
      expect(hasOpenDialog()).toBe(true);
      expect(hasUnsavedInput()).toBe(true);
    });

    it('an ARIA modal that is not a native dialog counts (Admin deactivate-user)', () => {
      document.body.innerHTML = '<div role="dialog" aria-modal="true"><button>Cancel</button></div>';
      expect(hasOpenDialog()).toBe(true);
      expect(hasUnsavedInput()).toBe(true);
    });

    it('a closed dialog does not', () => {
      document.body.innerHTML = '<dialog><input type="url" /></dialog>';
      expect(hasUnsavedInput()).toBe(false);
    });

    it('a typed-in text field is dirty; an untouched one with a default is not', () => {
      document.body.innerHTML = '<input id="a" type="text" value="seed" /><textarea id="t"></textarea>';
      expect(hasDirtyField()).toBe(false);
      (document.getElementById('a') as HTMLInputElement).value = 'seed edited';
      expect(hasDirtyField()).toBe(true);
      (document.getElementById('a') as HTMLInputElement).value = 'seed';
      (document.getElementById('t') as HTMLTextAreaElement).value = 'notes';
      expect(hasUnsavedInput()).toBe(true);
    });

    it('checkboxes and buttons never count as dirty text', () => {
      document.body.innerHTML = '<input type="checkbox" checked /><input type="submit" value="Go" />';
      const box = document.querySelector('input[type=checkbox]') as HTMLInputElement;
      box.checked = false;
      expect(hasDirtyField()).toBe(false);
    });

    it('a focused text field counts even when empty', () => {
      document.body.innerHTML = '<input id="f" type="text" />';
      (document.getElementById('f') as HTMLInputElement).focus();
      expect(hasUnsavedInput()).toBe(true);
    });

    it('a registered form counts', () => {
      setUnsaved('batch', true);
      expect(hasUnsavedInput()).toBe(true);
    });
  });
});
