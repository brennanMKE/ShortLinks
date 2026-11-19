<!--
  Login view. Two paths to authentication:

  1. Passkey autofill (conditional UI): on entering the sign-in card we ask the
     server for a discoverable challenge and fire navigator.credentials.get()
     with mediation: 'conditional'. The email <input
     autocomplete="username webauthn"> lets the browser surface matching
     passkeys inline in the autofill dropdown, so the user can sign in without
     clicking anything. The background get() resolves only when the user picks
     a passkey. Feature-detected via
     PublicKeyCredential.isConditionalMediationAvailable (webauthn.ts); where it
     is missing the button below is the only path.

  2. "Sign in with passkey" button: the email is optional. With one, we call
     GET /auth/login/start?email=… so the server can return a scoped
     allowCredentials list; without one the server issues a discoverable
     challenge. Then a modal get(), then post the assertion to
     /auth/login/finish.

  Layout (#0172, docs/ui-redesign.md §3.10): app icon above the title, one
  narrow card, and Register / Recover as text links under the card. They switch
  the card via the route's `mode` query (/login?mode=register|recover), which is
  also where a verify screen's "Send a new link" action lands. Errors carry
  their next action inline.

  On success we confirm the session via getMe(), set currentUser, and replace()
  to whatever route brought the user here signed-out — the `redirect` query
  param on this very route (#0120: an unauthenticated deep hash bounces to
  /login?redirect=…, and signing in returns there — see routing.ts's
  redirectTargetFrom).

  Register POSTs the email to /auth/register/start and shows a generic "check
  your email" confirmation (the server's response never reveals whether the
  address is registered).
-->
<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { currentUser } from '../lib/stores';
  import {
    getMe,
    getPublicHome,
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
  import { getRoute, replace } from '../lib/hashRouter.svelte';
  import { redirectTargetFrom } from '../lib/routing';
  import {
    loginFailure,
    loginModeFromQuery,
    loginModePath,
    failure,
    type Failure,
  } from '../lib/authScreens';
  import { createConditionalLogin } from '../lib/conditionalLogin';
  import Button from '../lib/Button.svelte';
  import Link from '../lib/Link.svelte';
  import Panel from '../lib/Panel.svelte';
  import AuthBrand from '../lib/AuthBrand.svelte';
  import { registerOptionVisible, type HomeInfo } from '../lib/home';

  let email = $state('');
  let signingIn = $state(false);
  let loginError = $state<Failure | null>(null);

  // Whether to offer Register (#0125): hidden until /api/public/home says
  // enrollment is open, so a closed instance (or a failed fetch) never shows a
  // sign-up the server would refuse. The server gate stays the enforcement.
  let homeInfo = $state<HomeInfo | null>(null);
  const canRegister = $derived(registerOptionVisible(homeInfo));
  // Known closed: home info loaded and enrollment is off. A failed fetch leaves
  // homeInfo null, which shows the form (the server gate still enforces).
  const registrationClosed = $derived(homeInfo !== null && !canRegister);
  // The redirect target the user arrived with survives the mode links.
  const redirect = $derived(getRoute().query.get('redirect'));

  // Which card is showing: sign in (default), register or recover. Lives in the
  // route so Back works and a verify screen can link straight to a form.
  const mode = $derived(loginModeFromQuery(Object.fromEntries(getRoute().query)));

  // Registration form state.
  let registerEmail = $state('');
  let registering = $state(false);
  let registerError = $state<Failure | null>(null);
  let registerSent = $state(false);

  // Recovery form state.
  let recoverEmail = $state('');
  let recovering = $state(false);
  let recoverError = $state<Failure | null>(null);
  let recoverSent = $state(false);

  // Background conditional-UI get(); stopped on unmount (or when an explicit
  // Sign in starts) so we never leave a dangling ceremony.
  const conditional = createConditionalLogin({
    available: conditionalMediationAvailable,
    run: (signal) => runCeremony('conditional', undefined, signal),
    onError: (err) => {
      const f = loginFailure(err);
      if (f) loginError = f;
    },
  });

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
    replace(redirectTargetFrom(getRoute().query));
    return true;
  }

  // ── Explicit "Sign in with passkey" (modal; email optional) ──────────────
  async function handleSignIn() {
    if (signingIn) return;
    conditional.stop();

    signingIn = true;
    loginError = null;
    try {
      const ok = await runCeremony('optional', email.trim() || undefined);
      if (!ok) {
        loginError = failure('No passkey was selected.', 'retry');
      }
    } catch (err) {
      const f = loginFailure(err);
      if (f) loginError = f;
    } finally {
      signingIn = false;
      // Only restart the background conditional-UI ceremony if we're still
      // ON the sign-in card — a successful sign-in already called replace()
      // above (synchronously updating the router's route, see
      // hashRouter.svelte.ts), which would otherwise race a background get()
      // against a component about to be destroyed.
      if (getRoute().path === '/login' && mode === 'signin') conditional.start();
    }
  }

  // ── Background conditional-UI ceremony (passkey autofill) ────────────────

  // The email field only exists on the sign-in card, so the autofill ceremony
  // follows it: started on entering sign-in, aborted on leaving.
  $effect(() => {
    if (mode === 'signin') {
      conditional.start();
    } else {
      conditional.stop();
    }
  });

  // ── Registration form ────────────────────────────────────────────────────
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
          registerError = failure('Registration is currently closed.', 'sign-in');
        } else if (err.status === 429) {
          registerError = failure('Too many requests. Wait a minute, then try again.', 'none');
        } else if (err.status === 400) {
          registerError = failure('Enter a valid email address.', 'none');
        } else {
          registerError = failure(err.message || 'Could not start registration.', 'none');
        }
      } else {
        registerError = failure('Could not reach the server. Check your connection.', 'none');
      }
    } finally {
      registering = false;
    }
  }

  // ── Recovery form ────────────────────────────────────────────────────────
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
          recoverError = failure('Too many requests. Wait a minute, then try again.', 'none');
        } else if (err.status === 400) {
          recoverError = failure('Enter a valid email address.', 'none');
        } else {
          recoverError = failure(err.message || 'Could not start account recovery.', 'none');
        }
      } else {
        recoverError = failure('Could not reach the server. Check your connection.', 'none');
      }
    } finally {
      recovering = false;
    }
  }

  onMount(() => {
    getPublicHome()
      .then((info) => {
        homeInfo = info;
      })
      .catch(() => {
        homeInfo = null;
      });
  });

  onDestroy(() => {
    conditional.stop();
  });
</script>

<main class="login-shell">
  <AuthBrand />

  <Panel>
    {#if mode === 'signin'}
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
          {signingIn ? 'Signing in…' : 'Sign in with passkey'}
        </Button>
        <p class="text-muted hint">
          Email is optional. Your browser may also offer your passkey in the field above.
        </p>

        {#if loginError}
          <p class="text-error inline-error" role="alert">
            {loginError.message}
            {#if loginError.action === 'retry'}
              <button type="button" class="text-action" onclick={handleSignIn}>
                {loginError.actionLabel}
              </button>
            {/if}
          </p>
        {/if}
      </form>
    {:else if mode === 'register'}
      {#if registrationClosed}
        <p class="text-error" role="alert">Registration is closed.</p>
      {:else if registerSent}
        <p class="text-notice" role="status">
          Check your email for a link to finish setting up your account.
        </p>
      {:else}
        <form
          class="sign-in-form"
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
          <Button type="submit" variant="primary" disabled={registering}>
            {registering ? 'Sending…' : 'Send link'}
          </Button>
          {#if registerError}
            <p class="text-error inline-error" role="alert">
              {registerError.message}
              {#if registerError.action === 'sign-in'}
                <Link to={loginModePath('signin', redirect)} class="text-action">{registerError.actionLabel}</Link>
              {/if}
            </p>
          {/if}
        </form>
      {/if}
    {:else if recoverSent}
      <p class="text-notice" role="status">
        If that email is registered, a recovery link has been sent.
      </p>
    {:else}
      <form
        class="sign-in-form"
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
        <Button type="submit" variant="primary" disabled={recovering}>
          {recovering ? 'Sending…' : 'Send recovery link'}
        </Button>
        {#if recoverError}
          <p class="text-error inline-error" role="alert">{recoverError.message}</p>
        {/if}
      </form>
    {/if}
  </Panel>

  <p class="alt text-muted">
    {#if mode !== 'signin'}
      <Link to={loginModePath('signin', redirect)}>Back to sign in</Link>
    {:else}
      {#if canRegister}
        <span>New here? <Link to={loginModePath('register', redirect)}>Register</Link></span>
      {/if}
      <span>Lost your passkey? <Link to={loginModePath('recover', redirect)}>Recover</Link></span>
    {/if}
  </p>
</main>

<style>
  .login-shell {
    max-width: 360px;
    margin: var(--space-6) auto;
    padding: 0 var(--space-4);
    /* On screens narrower than 360px (e.g. very small phones) ensure edge gutter */
    width: 100%;
  }
  .sign-in-form {
    display: flex;
    flex-direction: column;
    gap: var(--space-2);
  }
  .hint {
    font-size: var(--fs-sm);
    margin: 0;
  }
  .inline-error {
    margin: 0;
  }
  .text-action,
  :global(a.text-action) {
    background: none;
    border: 0;
    padding: 0;
    margin-left: var(--space-1);
    font: inherit;
    color: var(--accent);
    text-decoration: underline;
    cursor: pointer;
  }
  .alt {
    display: flex;
    flex-wrap: wrap;
    justify-content: center;
    gap: var(--space-1) var(--space-4);
    font-size: var(--fs-sm);
    margin: 0;
  }
  .alt :global(a) {
    color: var(--accent);
    text-decoration: underline;
  }

  @media (max-width: 480px) {
    /* Reduce top margin on very small screens */
    .login-shell {
      margin-top: var(--space-5);
    }
  }
</style>
