<!--
  Login view. Two paths to authentication:

  1. Passkey autofill (conditional UI): on mount we ask the server for a
     discoverable challenge and fire navigator.credentials.get() with
     mediation: 'conditional'. The email <input autocomplete="username webauthn">
     lets the browser surface matching passkeys inline in the autofill dropdown,
     so the user can sign in without clicking anything. The background get()
     resolves only when the user picks a passkey.

  2. Email-first / explicit "Sign in": the user types their email (optional) and
     clicks Sign in. We call GET /auth/login/start?email=… so the server can
     return a scoped allowCredentials list, then a modal get(), then post the
     assertion to /auth/login/finish.

  On success we confirm the session via getMe(), set currentUser, and switch the
  currentView store to "dashboard" (no router — navigation is a store write).

  The "Register" affordance opens a sub-form that POSTs the email to
  /auth/register/start and shows a generic "check your email" confirmation
  (the server's response never reveals whether the address is registered).
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { currentView, currentUser } from '../lib/stores';
  import {
    getMe,
    loginStart,
    loginFinish,
    registerStart,
    recoverStart,
    ApiError,
  } from '../lib/api';
  import {
    toPublicKeyRequestOptions,
    serializeAssertion,
    conditionalMediationAvailable,
  } from '../lib/webauthn';

  let email = $state('');
  let signingIn = $state(false);
  let loginError = $state<string | null>(null);

  // Registration sub-form state.
  let showRegister = $state(false);
  let registerEmail = $state('');
  let registering = $state(false);
  let registerError = $state<string | null>(null);
  let registerSent = $state(false);

  // Recovery sub-form state.
  let showRecover = $state(false);
  let recoverEmail = $state('');
  let recovering = $state(false);
  let recoverError = $state<string | null>(null);
  let recoverSent = $state(false);

  // AbortController for the background conditional-UI get(); aborted on unmount
  // (or when an explicit Sign in starts) so we never leave a dangling ceremony.
  let conditionalAbort: AbortController | null = null;

  const canSubmit = $derived(!signingIn);

  // ── Shared ceremony runner ──────────────────────────────────────────────
  // Drives a single navigator.credentials.get() and posts the result. Used by
  // both the conditional (autofill) and modal (button) paths; mediation and the
  // optional email are the only differences.
  async function runCeremony(
    mediation: CredentialMediationRequirement,
    withEmail: string | undefined,
    signal?: AbortSignal,
  ): Promise<boolean> {
    const assertion = await loginStart(withEmail);
    const publicKey = toPublicKeyRequestOptions(assertion.publicKey);

    const credential = (await navigator.credentials.get({
      publicKey,
      mediation,
      signal,
    })) as PublicKeyCredential | null;

    if (!credential) {
      // User dismissed the prompt or no credential was produced.
      return false;
    }

    await loginFinish(serializeAssertion(credential));
    const user = await getMe();
    currentUser.set(user);
    currentView.set('dashboard');
    return true;
  }

  function describeLoginError(err: unknown): string {
    if (err instanceof ApiError) {
      if (err.status === 403) return 'This account has been deactivated.';
      if (err.status === 429) return 'Too many attempts. Please wait a minute and try again.';
      if (err.status === 401) return 'Sign in failed. No matching passkey, or the request expired.';
      return err.message || 'Sign in failed. Please try again.';
    }
    if (err instanceof DOMException) {
      // AbortError is benign (we cancelled the background ceremony); NotAllowed
      // is the user dismissing the OS prompt.
      if (err.name === 'AbortError') return '';
      if (err.name === 'NotAllowedError') return '';
      return 'Passkey prompt was cancelled or unavailable.';
    }
    return 'Could not reach the server. Check your connection and try again.';
  }

  // ── Explicit "Sign in" (modal, email-first) ──────────────────────────────
  async function handleSignIn() {
    if (signingIn) return;
    // Cancel any in-flight conditional ceremony so the modal one can take over.
    conditionalAbort?.abort();
    conditionalAbort = null;

    signingIn = true;
    loginError = null;
    try {
      const ok = await runCeremony('optional', email || undefined);
      if (!ok) {
        loginError = 'No passkey was selected. Please try again.';
      }
    } catch (err) {
      const msg = describeLoginError(err);
      if (msg) loginError = msg;
    } finally {
      signingIn = false;
      // Re-arm the background autofill ceremony if the user is still here.
      if ($currentView === 'login') startConditional();
    }
  }

  // ── Background conditional-UI ceremony (passkey autofill) ────────────────
  async function startConditional() {
    if (!(await conditionalMediationAvailable())) return;
    conditionalAbort?.abort();
    const controller = new AbortController();
    conditionalAbort = controller;
    try {
      await runCeremony('conditional', undefined, controller.signal);
    } catch (err) {
      // Conditional UI failures are silent: the user can still use the button.
      const msg = describeLoginError(err);
      if (msg && !controller.signal.aborted) {
        // Only surface a genuine (non-abort) server/network error.
        loginError = msg;
      }
    }
  }

  // ── Registration sub-form ────────────────────────────────────────────────
  async function handleRegister() {
    if (registering) return;
    registering = true;
    registerError = null;
    try {
      await registerStart(registerEmail);
      registerSent = true;
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 403) {
          registerError = 'Registration is currently closed.';
        } else if (err.status === 429) {
          registerError = 'Too many requests. Please wait and try again.';
        } else if (err.status === 400) {
          registerError = 'Please enter a valid email address.';
        } else {
          registerError = err.message || 'Could not start registration.';
        }
      } else {
        registerError = 'Could not reach the server. Check your connection.';
      }
    } finally {
      registering = false;
    }
  }

  // ── Recovery sub-form ────────────────────────────────────────────────────
  async function handleRecover() {
    if (recovering) return;
    recovering = true;
    recoverError = null;
    try {
      await recoverStart(recoverEmail);
      recoverSent = true;
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 429) {
          recoverError = 'Too many requests. Please wait and try again.';
        } else if (err.status === 400) {
          recoverError = 'Please enter a valid email address.';
        } else {
          recoverError = err.message || 'Could not start account recovery.';
        }
      } else {
        recoverError = 'Could not reach the server. Check your connection.';
      }
    } finally {
      recovering = false;
    }
  }

  onMount(() => {
    startConditional();
  });

  onDestroy(() => {
    conditionalAbort?.abort();
    conditionalAbort = null;
  });
</script>

<main class="login">
  <header class="brand">
    <h1>go.sstools.co</h1>
    <p class="tagline">Sign in with your passkey</p>
  </header>

  <section class="card">
    <form
      onsubmit={(e) => {
        e.preventDefault();
        handleSignIn();
      }}
    >
      <label for="login-email">Email</label>
      <input
        id="login-email"
        type="email"
        name="email"
        autocomplete="username webauthn"
        placeholder="you@example.com"
        bind:value={email}
        disabled={signingIn}
      />

      <button type="submit" disabled={!canSubmit}>
        {signingIn ? 'Signing in…' : 'Sign in'}
      </button>

      {#if loginError}
        <p class="error" role="alert">{loginError}</p>
      {/if}
    </form>

    <div class="register-entry">
      {#if !showRegister}
        <button
          type="button"
          class="link"
          onclick={() => {
            showRegister = true;
          }}
        >
          Need an account? Register
        </button>
      {:else if registerSent}
        <p class="notice" role="status">
          Check your email for a link to finish setting up your account.
        </p>
      {:else}
        <form
          class="register-form"
          onsubmit={(e) => {
            e.preventDefault();
            handleRegister();
          }}
        >
          <label for="register-email">Register a new account</label>
          <input
            id="register-email"
            type="email"
            name="register-email"
            autocomplete="email"
            placeholder="you@example.com"
            bind:value={registerEmail}
            disabled={registering}
          />
          <div class="register-actions">
            <button type="submit" disabled={registering}>
              {registering ? 'Sending…' : 'Send link'}
            </button>
            <button
              type="button"
              class="link"
              onclick={() => {
                showRegister = false;
                registerError = null;
              }}
            >
              Cancel
            </button>
          </div>
          {#if registerError}
            <p class="error" role="alert">{registerError}</p>
          {/if}
        </form>
      {/if}
    </div>

    <div class="recover-entry">
      {#if !showRecover}
        <button
          type="button"
          class="link"
          onclick={() => {
            showRecover = true;
          }}
        >
          Lost your passkey? Recover account
        </button>
      {:else if recoverSent}
        <p class="notice" role="status">
          If that email is registered, a recovery link has been sent.
        </p>
      {:else}
        <form
          class="recover-form"
          onsubmit={(e) => {
            e.preventDefault();
            handleRecover();
          }}
        >
          <label for="recover-email">Recover your account</label>
          <input
            id="recover-email"
            type="email"
            name="recover-email"
            autocomplete="email"
            placeholder="you@example.com"
            bind:value={recoverEmail}
            disabled={recovering}
          />
          <div class="recover-actions">
            <button type="submit" disabled={recovering}>
              {recovering ? 'Sending…' : 'Send recovery link'}
            </button>
            <button
              type="button"
              class="link"
              onclick={() => {
                showRecover = false;
                recoverError = null;
              }}
            >
              Cancel
            </button>
          </div>
          {#if recoverError}
            <p class="error" role="alert">{recoverError}</p>
          {/if}
        </form>
      {/if}
    </div>
  </section>
</main>

<style>
  .login {
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
  .tagline {
    color: #666;
    margin: 0.25rem 0 0;
  }
  .card {
    border: 1px solid #e2e2e2;
    border-radius: 0.5rem;
    padding: 1.5rem;
  }
  form {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  label {
    font-weight: 600;
    font-size: 0.875rem;
  }
  input {
    padding: 0.5rem;
    border: 1px solid #ccc;
    border-radius: 0.375rem;
    font-size: 1rem;
  }
  button[type='submit'] {
    padding: 0.5rem;
    border: none;
    border-radius: 0.375rem;
    background: #1f6feb;
    color: #fff;
    font-size: 1rem;
    cursor: pointer;
  }
  button[type='submit']:disabled {
    opacity: 0.6;
    cursor: default;
  }
  .link {
    background: none;
    border: none;
    color: #1f6feb;
    cursor: pointer;
    padding: 0;
    font-size: 0.875rem;
  }
  .register-entry {
    margin-top: 1.25rem;
    padding-top: 1rem;
    border-top: 1px solid #eee;
  }
  .recover-entry {
    margin-top: 1rem;
    padding-top: 1rem;
    border-top: 1px solid #eee;
  }
  .recover-actions {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
  .register-actions {
    display: flex;
    align-items: center;
    gap: 0.75rem;
  }
  .error {
    color: #c0362c;
    font-size: 0.875rem;
    margin: 0.25rem 0 0;
  }
  .notice {
    color: #1a7f37;
    font-size: 0.875rem;
    margin: 0;
  }
</style>
