// Shared messages for the deactivate/undo flows (#0165): LinksList and
// LinkDetail deactivate with an Undo toast, LinkDetail's banner reactivates,
// and CampaignDetail names the campaign in its remove toast.

import { ApiError } from './api';

/** Message for a failed reactivation (also used as the toast's inline error). */
export function reactivateErrorMessage(err: unknown): string {
  if (err instanceof ApiError && err.status === 422) {
    return 'This link can no longer be reactivated because its destination is now blocked.';
  }
  return 'Could not reactivate this link. Please try again.';
}

export function deactivatedMessage(): string {
  return 'Link deactivated';
}

export function removedFromCampaignMessage(campaignName: string): string {
  return `Removed from ${campaignName}`;
}
