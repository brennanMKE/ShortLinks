// Dev-only gallery for the shared table pieces (#0155). Served by the Vite dev
// server at /table-demo.html; NOT part of the production build (vite build
// only builds index.html). Lets the components be seen before any view uses them.
import { mount } from 'svelte';
import '../app.css';
import TableDemo from './TableDemo.svelte';

mount(TableDemo, { target: document.getElementById('app')! });
