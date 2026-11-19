import { describe, it, expect } from 'vitest';
import { dialogKeyAction, trapTarget, restoreFocus } from './confirmDialog';

function fakeEl(connected = true) {
  const el = {
    isConnected: connected,
    focused: 0,
    focus() {
      el.focused++;
    },
  };
  return el;
}

describe('dialogKeyAction', () => {
  it('maps Escape to cancel and Tab to trap', () => {
    expect(dialogKeyAction('Escape')).toBe('cancel');
    expect(dialogKeyAction('Tab')).toBe('trap');
  });
  it('ignores other keys', () => {
    expect(dialogKeyAction('Enter')).toBeNull();
    expect(dialogKeyAction('a')).toBeNull();
  });
});

describe('trapTarget', () => {
  const a = { n: 'cancel' };
  const b = { n: 'confirm' };
  it('wraps Tab from the last control to the first', () => {
    expect(trapTarget([a, b], b, false)).toBe(a);
  });
  it('wraps Shift+Tab from the first control to the last', () => {
    expect(trapTarget([a, b], a, true)).toBe(b);
  });
  it('leaves movement between controls to the browser', () => {
    expect(trapTarget([a, b], a, false)).toBeNull();
    expect(trapTarget([a, b], b, true)).toBeNull();
  });
  it('pulls focus back in when it is outside the dialog', () => {
    const outside = { n: 'page' };
    expect(trapTarget([a, b], outside, false)).toBe(a);
    expect(trapTarget([a, b], null, true)).toBe(b);
  });
  it('does nothing without focusable controls', () => {
    expect(trapTarget([], null, false)).toBeNull();
  });
});

describe('restoreFocus', () => {
  it('returns focus to the trigger that opened the dialog', () => {
    const trigger = fakeEl();
    const fallback = fakeEl();
    expect(restoreFocus(trigger, fallback)).toBe(true);
    expect(trigger.focused).toBe(1);
    expect(fallback.focused).toBe(0);
  });
  it('uses the fallback when the trigger left the page', () => {
    const trigger = fakeEl(false);
    const fallback = fakeEl();
    expect(restoreFocus(trigger, fallback)).toBe(true);
    expect(trigger.focused).toBe(0);
    expect(fallback.focused).toBe(1);
  });
  it('reports false when there is nowhere to go', () => {
    expect(restoreFocus(null, null)).toBe(false);
  });
});

describe('no native confirm dialogs remain in web/src', () => {
  // Built from parts so this file does not match its own pattern.
  const pattern = new RegExp('(^|[^\\w.])(window\\.)?' + 'con' + 'firm\\(');
  const sources = import.meta.glob<string>('/src/**/*.{svelte,ts}', {
    query: '?raw',
    import: 'default',
    eager: true,
  });

  it('finds none in .svelte or .ts sources', () => {
    const hits: string[] = [];
    for (const [file, text] of Object.entries(sources)) {
      text.split('\n').forEach((line: string, i: number) => {
        if (pattern.test(line)) hits.push(`${file}:${i + 1}: ${line.trim()}`);
      });
    }
    expect(Object.keys(sources).length).toBeGreaterThan(20);
    expect(hits).toEqual([]);
  });
});
