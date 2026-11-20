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

/** Confirmation copy for the irreversible purge (#0183). */
export function purgeConfirmMessage(name: string): string {
  return `"${name}" will be permanently deleted along with its click history, and its destination URL is removed from the audit log. The short URL will stop working and its key can never be reused. This cannot be undone.`;
}

export function purgedMessage(): string {
  return 'Link permanently deleted';
}

export function removedFromCampaignMessage(campaignName: string): string {
  return `Removed from ${campaignName}`;
}
