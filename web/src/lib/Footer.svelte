<script lang="ts">
  import { APP_NAME } from './branding';
</script>

<footer class="app-footer">
  <p class="footer-text">
    <a
      href="https://github.com/brennanMKE/ShortLinks"
      target="_blank"
      rel="noopener noreferrer"
      class="footer-link"
    >{APP_NAME} — GitHub</a>
  </p>
</footer>

<style>
  .app-footer {
    text-align: center;
    padding: var(--space-4) var(--space-4) var(--space-5);
  }

  .footer-text {
    margin: 0;
    font-size: var(--fs-sm);
    color: var(--text-faint);
  }

  .footer-link {
    color: var(--text-faint);
    text-decoration: none;
  }

  .footer-link:hover {
    color: var(--text-muted);
    text-decoration: underline;
  }
</style>
