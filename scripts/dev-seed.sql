-- dev-seed.sql — realistic analytics data for looking at the Overview and
-- campaign screens against a real Postgres (dev.sh's STORAGE=json mode does
-- not mount /api/reports, so it cannot show them).
--
-- Seeds (relative to now(), so re-run on a fresh DB any day):
--   * user dev@example.com (admin)
--   * 7 campaigns, ~30 links (8 guides x 3 channels, like ocsf: one piece of
--     content shared on Mastodon, Bluesky and a newsletter, plus event links)
--     with long titles and UTM parameters
--   * ~5,000 clicks over the last 60 days including today, varied referrers,
--     UTM values, ~5% bots, a pool of 400 visitor hashes
--   * a session for the user whose cookie value is 'dev-seed-session-token'
--
-- Usage:
--   createdb -O shortlinks shortlinks_dev
--   migrate -path migrations -database "postgres://shortlinks@localhost/shortlinks_dev?sslmode=disable" up
--   psql shortlinks_dev -f scripts/dev-seed.sql
--   DATABASE_URL=postgres://shortlinks@localhost/shortlinks_dev?sslmode=disable \
--     BASE_URL=http://localhost:8188 WEBAUTHN_RP_ID=localhost \
--     WEBAUTHN_RP_ORIGIN=http://localhost:8188 SESSION_SECRET=dev-secret PORT=8188 \
--     go run ./cmd/shortlinks
--   then set cookie shortlinks_session=dev-seed-session-token on localhost:8188.
BEGIN;
SELECT setseed(0.42);

INSERT INTO users (email, is_admin) VALUES ('dev@example.com', true) ON CONFLICT DO NOTHING;
INSERT INTO sessions (user_id, token, expires_at, last_seen_at)
  SELECT id, 'dev-seed-session-token', now() + interval '30 days', now() FROM users WHERE email = 'dev@example.com'
  ON CONFLICT DO NOTHING;

DO $$
DECLARE
  uid bigint; cid bigint; lid bigint;
  g record; ch record; l record;
  n int; i int; ts timestamptz; src text; med text; ref text; ua text; bot boolean; weight numeric;
  campaigns text[][] := ARRAY[
    ['guides-2026-11','Guides 2026-11'], ['open-house','Open house'], ['spring-launch','Spring launch'],
    ['partner-outreach','Partner outreach'], ['newsletter-q4','Newsletter Q4'], ['poster-run','Poster run'],
    ['workshops','Workshop series']];
  guides text[][] := ARRAY[
    ['esp32-deep-sleep','ESP32 deep sleep, measured on three boards with a bench supply and a current probe'],
    ['measuring-current','Measuring current without blowing the fuse in your multimeter'],
    ['reading-datasheets','Reading a datasheet in ten minutes: the five tables that matter'],
    ['pcb-first-spin','Your first PCB spin: what we got wrong and what it cost us'],
    ['soldering-smd','Soldering SMD parts by hand without a hot-air station'],
    ['i2c-debugging','I2C debugging with a logic analyzer you already own'],
    ['battery-life','Battery life maths for sensors that wake once an hour'],
    ['enclosure-design','Designing a 3D printed enclosure that survives a drop test']];
  channels text[][] := ARRAY[['Mastodon guide post','mastodon','social','mastodon.social'],
    ['Bluesky guide post','bluesky','social','bsky.app'],['Newsletter November','newsletter','email','']];
  visitors bytea[];
BEGIN
  SELECT id INTO uid FROM users WHERE email = 'dev@example.com';
  FOR i IN 1..array_length(campaigns,1) LOOP
    INSERT INTO campaigns (user_id, name, slug) VALUES (uid, campaigns[i][2], campaigns[i][1]) ON CONFLICT DO NOTHING;
  END LOOP;
  SELECT array_agg(decode(md5('visitor' || s), 'hex')) INTO visitors FROM generate_series(1, 400) s;

  -- 8 guides x 3 channels
  FOR i IN 1..array_length(guides,1) LOOP
    FOR n IN 1..3 LOOP
      SELECT id INTO cid FROM campaigns WHERE user_id = uid AND slug = CASE WHEN i <= 4 THEN 'guides-2026-11' WHEN i <= 6 THEN 'newsletter-q4' ELSE 'partner-outreach' END;
      INSERT INTO links (user_id, key, destination_url, title, campaign_id, utm_source, utm_medium, utm_campaign, utm_content, placement, created_at)
      VALUES (uid, 'g' || i || 'c' || n, 'https://opencircuitsf.com/guides/' || guides[i][1] || '?utm_source=' || channels[n][2] || '&utm_medium=' || channels[n][3] || '&utm_campaign=guides-2026-11&utm_content=guide-' || i,
              guides[i][2] || ' — ' || channels[n][1] || ' ' || (80 + i), cid, channels[n][2], channels[n][3], 'guides-2026-11', 'guide-' || i, '', now() - interval '45 days' + (i * interval '2 days'));
    END LOOP;
  END LOOP;
  -- events, posters and a few unassigned links
  INSERT INTO links (user_id, key, destination_url, title, campaign_id, utm_source, utm_medium, utm_campaign, placement, created_at)
  SELECT uid, 'ev' || s, 'https://opencircuitsf.com/events/' || (ARRAY['open-house','soldering-night','kicad-workshop','hardware-happy-hour','repair-cafe','makers-market'])[s],
         (ARRAY['Open house at the shop: tour the lab, meet the members and try the laser cutter','Soldering night for beginners: bring a kit or buy one at the door','KiCad workshop, two evenings: schematic capture to a fabricated board','Hardware happy hour with demos from local makers and short lightning talks','Repair cafe: bring something broken, leave with it fixed (or a good story)','Makers market and swap meet in the courtyard, free table for members'])[s],
         (SELECT id FROM campaigns WHERE user_id = uid AND slug = (ARRAY['open-house','workshops','workshops','spring-launch','poster-run','poster-run'])[s]),
         (ARRAY['poster','poster','flyer','instagram','poster','flyer'])[s], (ARRAY['print','print','print','social','print','print'])[s], 'events-2026',
         (ARRAY['18th & Texas board','Mission St pole','Library corkboard','','Cafe window','Hardware store'])[s], now() - interval '40 days' + (s * interval '1 day')
  FROM generate_series(1, 6) s;
  INSERT INTO links (user_id, key, destination_url, title, created_at)
  VALUES (uid, 'about', 'https://opencircuitsf.com/about', 'About Open Circuit', now() - interval '60 days'),
         (uid, 'donate', 'https://opencircuitsf.com/donate?utm_source=site', 'Donate to keep the lights on', now() - interval '58 days'),
         (uid, 'quiet', 'https://opencircuitsf.com/old-page', 'An old page that nobody visits any more', now() - interval '55 days');

  -- clicks: per link, a popularity weight and a growth bias toward recent days
  FOR l IN SELECT id, key, utm_source, utm_medium, campaign_id FROM links WHERE user_id = uid LOOP
    weight := CASE WHEN l.key = 'quiet' THEN 0 ELSE 0.4 + random() * 1.6 END;
    IF l.key LIKE 'g1c%' OR l.key LIKE 'g2c%' THEN weight := weight * 2; END IF;
    FOR i IN 1..floor(weight * 55)::int LOOP
      -- skew: ~65% in last 30 days, some today
      IF random() < 0.65 THEN ts := now() - (random() * 30) * interval '1 day';
      ELSE ts := now() - (30 + random() * 30) * interval '1 day'; END IF;
      IF random() < 0.006 THEN ts := date_trunc('day', now()) + random() * (now() - date_trunc('day', now())); END IF;
      bot := random() < 0.05;
      ref := (ARRAY['https://mastodon.social/','https://bsky.app/','https://news.ycombinator.com/','https://www.google.com/','','https://t.co/x','https://www.reddit.com/r/esp32/'])[1 + floor(random()*7)::int];
      ua := CASE WHEN bot THEN 'Googlebot/2.1' ELSE 'Mozilla/5.0 (Macintosh) Safari/605' END;
      src := COALESCE(NULLIF(l.utm_source, ''), (ARRAY['site','direct','',''])[1 + floor(random()*4)::int]);
      INSERT INTO clicks (link_id, clicked_at, user_agent, referer, utm_source, utm_medium, campaign_id, is_bot, ip_hash)
      VALUES (l.id, ts, ua, NULLIF(ref, ''), NULLIF(src, ''), NULLIF(l.utm_medium, ''), l.campaign_id, bot, visitors[1 + floor(random()*400)::int]);
    END LOOP;
  END LOOP;
END $$;
COMMIT;
