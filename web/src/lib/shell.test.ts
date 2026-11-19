import { describe, it, expect } from 'vitest';
import { navTabs, avatarInitial, isNewLinkShortcut, type ShortcutEvent } from './shell';

const key = (over: Partial<ShortcutEvent> = {}): ShortcutEvent => ({
  key: 'n',
  ctrlKey: false,
  metaKey: false,
  altKey: false,
  target: { tagName: 'BODY' },
  ...over,
});

describe('navTabs', () => {
  it('lists Overview, Links, Campaigns for a normal user', () => {
    expect(navTabs(false).map((t) => t.to)).toEqual(['/', '/links', '/campaigns']);
  });
  it('adds Admin last for an admin', () => {
    expect(navTabs(true).map((t) => t.label)).toEqual(['Overview', 'Links', 'Campaigns', 'Admin']);
  });
});

describe('avatarInitial', () => {
  it('uppercases the first character', () => expect(avatarInitial('brennan@x.com')).toBe('B'));
  it('falls back to ?', () => {
    expect(avatarInitial('')).toBe('?');
    expect(avatarInitial(undefined)).toBe('?');
  });
});

describe('isNewLinkShortcut', () => {
  it('fires on a bare n', () => expect(isNewLinkShortcut(key())).toBe(true));
  it('ignores other keys', () => expect(isNewLinkShortcut(key({ key: 'm' }))).toBe(false));
  it('ignores modified n', () => {
    expect(isNewLinkShortcut(key({ metaKey: true }))).toBe(false);
    expect(isNewLinkShortcut(key({ ctrlKey: true }))).toBe(false);
    expect(isNewLinkShortcut(key({ altKey: true }))).toBe(false);
  });
  it('ignores typing in text fields', () => {
    for (const tagName of ['INPUT', 'TEXTAREA', 'SELECT']) {
      expect(isNewLinkShortcut(key({ target: { tagName } }))).toBe(false);
    }
    expect(isNewLinkShortcut(key({ target: { tagName: 'DIV', isContentEditable: true } }))).toBe(false);
  });
  it('fires with a link focused', () => {
    expect(isNewLinkShortcut(key({ target: { tagName: 'A' } }))).toBe(true);
  });
});
