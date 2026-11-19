import { describe, it, expect } from 'vitest';
import { ApiError } from './api';
import { reactivateErrorMessage, removedFromCampaignMessage } from './linkActions';

describe('linkActions', () => {
  it('explains a filter denial on reactivation', () => {
    expect(reactivateErrorMessage(new ApiError(422, 'x', { error: 'destination_denied' }))).toMatch(/blocked/);
  });
  it('falls back to a generic message', () => {
    expect(reactivateErrorMessage(new Error('boom'))).toMatch(/Could not reactivate/);
  });
  it('names the campaign', () => {
    expect(removedFromCampaignMessage('Guides 2026-11')).toBe('Removed from Guides 2026-11');
  });
});
