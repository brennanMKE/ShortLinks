// purgeLink (#0183): the irreversible delete must carry ?purge=true and the
// X-Confirm-Purge header, and its confirmation copy must say it cannot be undone.
import { afterEach, describe, expect, it, vi } from 'vitest';
import { purgeLink } from './api';
import { purgeConfirmMessage } from './linkActions';

afterEach(() => vi.unstubAllGlobals());

describe('purgeLink', () => {
  it('sends DELETE with purge=true and the confirming header', async () => {
    const fetchMock = vi.fn(async () => ({
      ok: true,
      status: 200,
      text: async () => JSON.stringify({ message: 'Link purged', key: 'a b', purged: true }),
    }));
    vi.stubGlobal('fetch', fetchMock);
    const res = await purgeLink('a b');
    expect(res.purged).toBe(true);
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/links/a%20b?purge=true');
    expect(init.method).toBe('DELETE');
    expect((init.headers as Record<string, string>)['X-Confirm-Purge']).toBe('a b');
  });
});

describe('purgeConfirmMessage', () => {
  it('names the link and says it cannot be undone', () => {
    const m = purgeConfirmMessage('Invite');
    expect(m).toContain('"Invite"');
    expect(m).toContain('cannot be undone');
  });
});
