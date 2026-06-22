<!--
  RecoverVerify — landing view for the account-recovery magic-link.

  The user arrives here after clicking the link in their recovery email.
  App.svelte has already parsed the ?token= query param from the landing URL
  (/recover/verify?token=…) and written it to the pendingVerifyToken store.

  Flow:
    1. On mount: call GET /auth/recover/verify?token=… to validate the token
       and fetch the WebAuthn PublicKeyCredentialCreationOptions.
    2. Call navigator.credentials.create() with the decoded options.
    3. POST the serialized attestation to /auth/recover/finish?token=….
    4. On success: call GET /api/me to confirm the session, set currentUser,
       clear pendingVerifyToken, and navigate to "dashboard".
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { pendingVerifyToken, currentView, currentUser } from '../lib/stores';
  import { recoverVerify, recoverFinish, getMe, ApiError } from '../lib/api';
  import {
    toPublicKeyCreationOptions,
    serializeAttestation,
  } from '../lib/webauthn';
  import { get } from 'svelte/store';
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';
  import { APP_NAME } from '../lib/branding';

  type Status = 'verifying' | 'creating' | 'finishing' | 'error';

  let status = $state<Status>('verifying');
  let errorMessage = $state<string | null>(null);
  let token = $state<string | null>(null);

  onMount(async () => {
    token = get(pendingVerifyToken);
    if (!token) {
      status = 'error';
      errorMessage = 'No recovery token found. Please check your email link and try again.';
      return;
    }

    // Step 1 — fetch creation options.
    let serverOptions;
    try {
      serverOptions = await recoverVerify(token);
    } catch (err) {
      status = 'error';
      if (err instanceof ApiError) {
        if (err.status === 410 || err.status === 400) {
          errorMessage =
            'This recovery link has expired or is invalid. Please request a new one.';
        } else if (err.status === 401) {
          errorMessage = 'Recovery failed. Please request a new recovery email.';
        } else {
          errorMessage = `Recovery error: ${err.message}`;
        }
      } else {
        errorMessage = 'An unexpected error occurred. Please try again.';
      }
      return;
    }

    // Step 2 — run the browser credential-creation ceremony.
    status = 'creating';
    let credential: PublicKeyCredential | null = null;
    try {
      const publicKey = toPublicKeyCreationOptions(serverOptions.publicKey);
      credential = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null;
    } catch (err) {
      status = 'error';
      if (err instanceof DOMException) {
        if (err.name === 'NotAllowedError') {
          errorMessage =
            'Passkey creation was cancelled. Please try again when you are ready.';
        } else if (err.name === 'InvalidStateError') {
          errorMessage =
            'A passkey for this account is already registered on this device.';
        } else {
          errorMessage = 'The passkey prompt was unavailable. Please try again.';
        }
      } else {
        errorMessage = 'An unexpected error occurred during passkey creation. Please try again.';
      }
      return;
    }

    if (!credential) {
      status = 'error';
      errorMessage = 'No passkey was created. Please try again.';
      return;
    }

    // Step 3 — POST attestation to finish recovery.
    status = 'finishing';
    try {
      const attestation = serializeAttestation(credential);
      await recoverFinish(token, attestation);
    } catch (err) {
      status = 'error';
      if (err instanceof ApiError) {
        if (err.status === 400) {
          errorMessage =
            'Recovery could not be completed. The link may have expired — please request a new one.';
        } else {
          errorMessage = `Recovery failed: ${err.message}`;
        }
      } else {
        errorMessage = 'Could not reach the server. Check your connection and try again.';
      }
      return;
    }

    // Step 4 — confirm session, set user, navigate.
    try {
      const user = await getMe();
      currentUser.set(user);
      pendingVerifyToken.set(null);
      history.replaceState({}, '', '/');
      currentView.set('dashboard');
    } catch {
      status = 'error';
      errorMessage =
        'Your account was recovered but we could not load your profile. Try signing in.';
    }
  });

  function goToLogin() {
    pendingVerifyToken.set(null);
    currentView.set('login');
  }
</script>

<main class="verify-shell">
  <header class="brand">
    <h1 class="brand-title">{APP_NAME}</h1>
  </header>

  <Panel>
    {#if status === 'verifying'}
      <p class="text-muted">Verifying your recovery link…</p>
    {:else if status === 'creating'}
      <p>Creating your new passkey…</p>
      <p class="text-muted">Follow the prompt from your device to register a passkey.</p>
    {:else if status === 'finishing'}
      <p class="text-muted">Finishing account recovery…</p>
    {:else if status === 'error'}
      <p class="text-error" role="alert">{errorMessage}</p>
      <Button onclick={goToLogin}>Back to sign in</Button>
    {/if}
  </Panel>
</main>

<style>
  .verify-shell {
    max-width: 360px;
    margin: var(--space-6) auto;
    padding: 0 var(--space-4);
    width: 100%;
  }
  .brand {
    text-align: center;
    margin-bottom: var(--space-5);
  }
  .brand-title {
    font-size: var(--fs-xl);
    font-weight: 600;
    margin: 0;
  }

  @media (max-width: 480px) {
    .verify-shell {
      margin-top: var(--space-5);
    }
  }
</style>
