<!--
  RecoverVerify — landing view for the account-recovery magic-link.

  The user arrives here after clicking the link in their recovery email.
  App.svelte has already parsed the ?token= query param from the landing URL
  (/recover/verify?token=…) and written it to the pendingVerifyToken store.

  On mount this view calls GET /auth/recover/verify?token=… to validate the
  token and fetch the WebAuthn PublicKeyCredentialCreationOptions from the
  server. The three possible outcomes are shown inline:

    • "verifying…" — the fetch is in flight
    • "ready"      — options arrived; ceremony can begin  (TODO #0043)
    • "error"      — token is invalid, expired, or the request failed

  The actual navigator.credentials.create() ceremony (reusing helpers from
  #0042), the POST to /auth/recover/finish, and the session set/navigation are
  implemented in #0043. This scaffold proves the round-trip.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { pendingVerifyToken, currentView } from '../lib/stores';
  import { recoverVerify, ApiError } from '../lib/api';
  import { get } from 'svelte/store';

  type Status = 'verifying' | 'ready' | 'error';

  let status = $state<Status>('verifying');
  let errorMessage = $state<string | null>(null);

  // The server creation options, held for #0043.
  // TODO #0043: type as ServerCredentialCreation (from #0042) and drive
  // navigator.credentials.create(), then POST to /auth/recover/finish.
  let creationOptions = $state<unknown>(null);

  onMount(async () => {
    const token = get(pendingVerifyToken);
    if (!token) {
      status = 'error';
      errorMessage = 'No recovery token found. Please check your email link and try again.';
      return;
    }

    try {
      const options = await recoverVerify(token);
      creationOptions = options;
      status = 'ready';
      // TODO #0043: call navigator.credentials.create() (reusing helpers from
      // #0042), POST the attestation to /auth/recover/finish, set currentUser,
      // and navigate to 'dashboard'.
    } catch (err) {
      status = 'error';
      if (err instanceof ApiError) {
        if (err.status === 410 || err.status === 400) {
          errorMessage = 'This recovery link has expired or is invalid. Please request a new one.';
        } else if (err.status === 401) {
          errorMessage = 'Recovery failed. Please request a new recovery email.';
        } else {
          errorMessage = `Recovery error: ${err.message}`;
        }
      } else {
        errorMessage = 'An unexpected error occurred. Please try again.';
      }
    }
  });

  function goToLogin() {
    pendingVerifyToken.set(null);
    currentView.set('login');
  }
</script>

<main>
  <h1>Recover your account</h1>

  {#if status === 'verifying'}
    <p>Verifying your recovery link…</p>
  {:else if status === 'ready'}
    <!--
      Token is valid and creation options have been fetched.
      #0043 will replace this placeholder with the passkey creation ceremony.
    -->
    <p>Your recovery link is valid. Setting up your new passkey…</p>
    <p><em>(Passkey creation ceremony coming in #0043.)</em></p>
  {:else if status === 'error'}
    <p role="alert">{errorMessage}</p>
    <button onclick={goToLogin}>Back to sign in</button>
  {/if}
</main>
