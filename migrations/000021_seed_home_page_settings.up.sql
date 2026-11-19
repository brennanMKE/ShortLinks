-- Public home page content, chosen in Admin -> Settings (#0174).
INSERT INTO settings (key, value, updated_at) VALUES
  ('home_mode',  'default', now()),
  ('home_title', '',        now()),
  ('home_about', '',        now())
ON CONFLICT DO NOTHING;
