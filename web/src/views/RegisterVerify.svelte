<!--
  RegisterVerify — landing view for the registration magic-link.

  The user arrives here after clicking the link in their verification email.
  App.svelte has already parsed the ?token= query param from the landing URL
  (/register/verify?token=…) and written it to the pendingVerifyToken store.

  On mount this view calls GET /auth/register/verify?token=… to validate the
  token and fetch the WebAuthn PublicKeyCredentialCreationOptions from the
  server. The three possible outcomes are shown inline:

    • "verifying…" — the fetch is in flight
    • "ready"      — options arrived; ceremony can begin  (TODO #0042)
    • "error"      — token is invalid, expired, or the request failed

  The actual navigator.credentials.create() ceremony, the POST to
  /auth/register/finish, and the session set/dashboard navigation are
  implemented in #0042. This scaffold proves the round-trip: a valid token
  fetches options and a clear "ready" state is displayed; an invalid/expired
  token shows a human-readable error without a raw JSON blob.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { pendingVerifyToken, currentView } from '../lib/stores';
  import { registerVerify, ApiError } from '../lib/api';
  import { get } from 'svelte/store';

  type Status = 'verifying' | 'ready' | 'error';

  let status = $state<Status>('verifying');
  let errorMessage = $state<string | null>(null);

  // The server creation options, held for #0042 to use once the ceremony is
  // wired up. Typed as unknown here because the ServerCredentialCreation type
  // lives in webauthn.ts and will be introduced by #0042.
  // TODO #0042: type as ServerCredentialCreation and drive navigator.credentials.create()
  let creationOptions = $state<unknown>(null);

  onMount(async () => {
    const token = get(pendingVerifyToken);
    if (!token) {
      status = 'error';
      errorMessage = 'No verification token found. Please check your email link and try again.';
      return;
    }

    try {
      const options = await registerVerify(token);
      creationOptions = options;
      status = 'ready';
      // TODO #0042: call navigator.credentials.create() here, POST the
      // attestation to /auth/register/finish, set currentUser, and navigate to
      // 'dashboard'. The creationOptions variable above already holds the
      // PublicKeyCredentialCreationOptions returned by the server.
    } catch (err) {
      status = 'error';
      if (err instanceof ApiError) {
        if (err.status === 410 || err.status === 400) {
          errorMessage = 'This verification link has expired or is invalid. Please request a new one.';
        } else if (err.status === 401) {
          errorMessage = 'Verification failed. Please request a new registration email.';
        } else {
          errorMessage = `Verification error: ${err.message}`;
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
  <h1>Verify your account</h1>

  {#if status === 'verifying'}
    <p>Verifying your link…</p>
  {:else if status === 'ready'}
    <!--
      Token is valid and creation options have been fetched.
      #0042 will replace this placeholder with the passkey creation ceremony.
    -->
    <p>Your link is valid. Setting up your passkey…</p>
    <p><em>(Passkey creation ceremony coming in #0042.)</em></p>
  {:else if status === 'error'}
    <p role="alert">{errorMessage}</p>
    <button onclick={goToLogin}>Back to sign in</button>
  {/if}
</main>
