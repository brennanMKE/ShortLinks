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
  import Button from '../lib/Button.svelte';
  import Panel from '../lib/Panel.svelte';

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
      if (err.name === 'AbortError') return '';
      if (err.name === 'NotAllowedError') return '';
      return 'Passkey prompt was cancelled or unavailable.';
    }
    return 'Could not reach the server. Check your connection and try again.';
  }

  // ── Explicit "Sign in" (modal, email-first) ──────────────────────────────
  async function handleSignIn() {
    if (signingIn) return;
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
      const msg = describeLoginError(err);
      if (msg && !controller.signal.aborted) {
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

<main class="login-shell">
  <header class="brand">
    <h1 class="brand-title">go.sstools.co</h1>
    <p class="text-muted tagline">Sign in with your passkey</p>
  </header>

  <Panel>
    <form
      class="sign-in-form"
      onsubmit={(e) => {
        e.preventDefault();
        handleSignIn();
      }}
    >
      <div class="field">
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
      </div>

      <Button type="submit" variant="primary" disabled={!canSubmit}>
        {signingIn ? 'Signing in…' : 'Sign in'}
      </Button>

      {#if loginError}
        <p class="text-error" role="alert">{loginError}</p>
      {/if}
    </form>

    <hr class="divider" />

    <div class="sub-section">
      {#if !showRegister}
        <Button
          variant="subtle"
          onclick={() => {
            showRegister = true;
          }}
        >
          Need an account? Register
        </Button>
      {:else if registerSent}
        <p class="text-notice" role="status">
          Check your email for a link to finish setting up your account.
        </p>
      {:else}
        <form
          onsubmit={(e) => {
            e.preventDefault();
            handleRegister();
          }}
        >
          <div class="field">
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
          </div>
          <div class="row">
            <Button type="submit" variant="primary" disabled={registering}>
              {registering ? 'Sending…' : 'Send link'}
            </Button>
            <Button
              variant="subtle"
              onclick={() => {
                showRegister = false;
                registerError = null;
              }}
            >
              Cancel
            </Button>
          </div>
          {#if registerError}
            <p class="text-error" role="alert">{registerError}</p>
          {/if}
        </form>
      {/if}
    </div>

    <div class="sub-section">
      {#if !showRecover}
        <Button
          variant="subtle"
          onclick={() => {
            showRecover = true;
          }}
        >
          Lost your passkey? Recover account
        </Button>
      {:else if recoverSent}
        <p class="text-notice" role="status">
          If that email is registered, a recovery link has been sent.
        </p>
      {:else}
        <form
          onsubmit={(e) => {
            e.preventDefault();
            handleRecover();
          }}
        >
          <div class="field">
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
          </div>
          <div class="row">
            <Button type="submit" variant="primary" disabled={recovering}>
              {recovering ? 'Sending…' : 'Send recovery link'}
            </Button>
            <Button
              variant="subtle"
              onclick={() => {
                showRecover = false;
                recoverError = null;
              }}
            >
              Cancel
            </Button>
          </div>
          {#if recoverError}
            <p class="text-error" role="alert">{recoverError}</p>
          {/if}
        </form>
      {/if}
    </div>
  </Panel>
</main>

<style>
  .login-shell {
    max-width: 360px;
    margin: var(--space-6) auto;
    padding: 0 var(--space-4);
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
  .tagline {
    margin: var(--space-1) 0 0;
  }
  .sign-in-form {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .sub-section {
    margin-top: var(--space-3);
  }
</style>
