package handlers

// testBaseURL stands in for the deployment's configured BASE_URL (#0117),
// which the handlers now take as a constructor argument instead of reading a
// compiled-in domain out of internal/qr. Tests that assert on a short URL or
// a QR payload build their expectation from this same value, so none of them
// pins a real production hostname.
const testBaseURL = "https://example.test"
