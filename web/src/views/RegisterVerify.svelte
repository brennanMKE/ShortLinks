<!--
  RegisterVerify — landing view for the registration magic-link.

  The user arrives here after clicking the link in their verification email.
  App.svelte has already parsed the ?token= query param from the landing URL
  (/register/verify?token=…) and written it to the pendingVerifyToken store.

  Flow:
    1. On mount: call GET /auth/register/verify?token=… to validate the token
       and fetch the WebAuthn PublicKeyCredentialCreationOptions.
    2. Call navigator.credentials.create() with the decoded options.
    3. POST the serialized attestation to /auth/register/finish?token=….
    4. On success: call GET /api/me to confirm the session, set currentUser,
       clear pendingVerifyToken, and navigate to "dashboard".

  Error states shown inline:
    • expired/invalid token   → descriptive message + back-to-login link
    • ceremony cancelled      → "NotAllowedError" from the OS prompt
    • generic failure         → catchall with retry prompt

  Admin promotion (ADMIN_EMAIL match or first user) is handled by the server
  inside FinishRegistration — the SPA simply navigates to "dashboard" and the
  Admin tab appears when currentUser.is_admin is true.
-->
<script lang="ts">
  import { onMount } from 'svelte';
  import { pendingVerifyToken, currentView, currentUser } from '../lib/stores';
  import { registerVerify, registerFinish, getMe, ApiError } from '../lib/api';
  import {
    toPublicKeyCreationOptions,
    serializeAttestation,
  } from '../lib/webauthn';
  import { get } from 'svelte/store';

  type Status = 'verifying' | 'creating' | 'finishing' | 'error';

  let status = $state<Status>('verifying');
  let errorMessage = $state<string | null>(null);
  // Stash the token for the finish POST; it is the same token used for verify.
  let token = $state<string | null>(null);

  onMount(async () => {
    token = get(pendingVerifyToken);
    if (!token) {
      status = 'error';
      errorMessage = 'No verification token found. Please check your email link and try again.';
      return;
    }

    // Step 1 — fetch creation options.
    let serverOptions;
    try {
      serverOptions = await registerVerify(token);
    } catch (err) {
      status = 'error';
      if (err instanceof ApiError) {
        if (err.status === 410 || err.status === 400) {
          errorMessage =
            'This verification link has expired or is invalid. Please request a new one.';
        } else if (err.status === 401) {
          errorMessage = 'Verification failed. Please request a new registration email.';
        } else {
          errorMessage = `Verification error: ${err.message}`;
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
          // A credential for this user is already registered on this device.
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

    // Step 3 — POST attestation to finish registration.
    status = 'finishing';
    try {
      const attestation = serializeAttestation(credential);
      await registerFinish(token, attestation);
    } catch (err) {
      status = 'error';
      if (err instanceof ApiError) {
        if (err.status === 400) {
          errorMessage =
            'Registration could not be completed. The link may have expired — please request a new one.';
        } else {
          errorMessage = `Registration failed: ${err.message}`;
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
      // Replace the consumed-token URL so a reload routes through /api/me
      // (valid session → dashboard) instead of replaying the spent token.
      history.replaceState({}, '', '/');
      currentView.set('dashboard');
    } catch {
      // The session cookie was set by the finish endpoint; a /api/me failure
      // is very unlikely but we handle it gracefully rather than leaving the
      // user on a blank screen.
      status = 'error';
      errorMessage =
        'Your account was created but we could not load your profile. Try signing in.';
    }
  });

  function goToLogin() {
    pendingVerifyToken.set(null);
    currentView.set('login');
  }
</script>

<main class="register-verify">
  <header class="brand">
    <h1>go.sstools.co</h1>
  </header>

  <section class="card">
    {#if status === 'verifying'}
      <p class="status">Verifying your link…</p>
    {:else if status === 'creating'}
      <p class="status">Creating your passkey…</p>
      <p class="hint">Follow the prompt from your device to register a passkey.</p>
    {:else if status === 'finishing'}
      <p class="status">Finishing registration…</p>
    {:else if status === 'error'}
      <p class="error" role="alert">{errorMessage}</p>
      <button onclick={goToLogin}>Back to sign in</button>
    {/if}
  </section>
</main>

<style>
  .register-verify {
    max-width: 24rem;
    margin: 4rem auto;
    padding: 0 1rem;
  }
  .brand {
    text-align: center;
    margin-bottom: 1.5rem;
  }
  .brand h1 {
    font-size: 1.5rem;
    margin: 0;
  }
  .card {
    border: 1px solid #e2e2e2;
    border-radius: 0.5rem;
    padding: 1.5rem;
    display: flex;
    flex-direction: column;
    gap: 0.75rem;
  }
  .status {
    color: #444;
    margin: 0;
  }
  .hint {
    color: #666;
    font-size: 0.875rem;
    margin: 0;
  }
  .error {
    color: #c0362c;
    font-size: 0.9rem;
    margin: 0;
  }
  button {
    align-self: flex-start;
    padding: 0.4rem 0.9rem;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    background: #fff;
    cursor: pointer;
    font-size: 0.875rem;
  }
  button:hover {
    background: #f5f5f5;
  }
</style>
