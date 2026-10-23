(function () {
  "use strict";

  var meta = document.querySelector('meta[name="csrf-token"]');
  var csrf = meta ? meta.getAttribute("content") : "";

  function toB64url(buf) {
    var bytes = new Uint8Array(buf);
    var s = "";
    for (var i = 0; i < bytes.length; i++) s += String.fromCharCode(bytes[i]);
    return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function fromB64url(s) {
    if (s == null) return null;
    s = String(s).replace(/-/g, "+").replace(/_/g, "/");
    while (s.length % 4) s += "=";
    var raw = atob(s);
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
    return out.buffer;
  }

  function decodeCreation(options) {
    var pk = options.publicKey;
    pk.challenge = fromB64url(pk.challenge);
    if (pk.user && pk.user.id) pk.user.id = fromB64url(pk.user.id);
    if (pk.excludeCredentials) {
      pk.excludeCredentials = pk.excludeCredentials.map(function (c) {
        return { type: c.type, id: fromB64url(c.id), transports: c.transports };
      });
    }
    return pk;
  }

  function decodeRequest(options) {
    var pk = options.publicKey;
    pk.challenge = fromB64url(pk.challenge);
    if (pk.allowCredentials) {
      pk.allowCredentials = pk.allowCredentials.map(function (c) {
        return { type: c.type, id: fromB64url(c.id), transports: c.transports };
      });
    }
    return pk;
  }

  function encodeCredential(cred) {
    var response;
    if (cred.response.attestationObject) {
      response = {
        clientDataJSON: toB64url(cred.response.clientDataJSON),
        attestationObject: toB64url(cred.response.attestationObject)
      };
      if (cred.response.getTransports) {
        try { response.transports = cred.response.getTransports(); } catch (e) {}
      }
    } else {
      response = {
        clientDataJSON: toB64url(cred.response.clientDataJSON),
        authenticatorData: toB64url(cred.response.authenticatorData),
        signature: toB64url(cred.response.signature),
        userHandle: cred.response.userHandle ? toB64url(cred.response.userHandle) : null
      };
    }
    return {
      id: cred.id,
      rawId: toB64url(cred.rawId),
      type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
      response: response
    };
  }

  async function postJSON(url, body) {
    var res = await fetch(url, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: JSON.stringify(body)
    });
    var data = null;
    try { data = await res.json(); } catch (e) {}
    if (!res.ok) {
      var msg = (data && data.error) || "Die Anfrage ist fehlgeschlagen.";
      throw new Error(msg);
    }
    return data;
  }

  function showError(root, message) {
    var el = root ? root.querySelector("[data-passkey-error]") : null;
    if (el) {
      el.textContent = message;
      el.classList.remove("hidden");
    } else {
      alert(message);
    }
  }

  async function login() {
    var emailEl = document.getElementById("email");
    var email = emailEl ? emailEl.value.trim() : "";
    if (!email) {
      showError(document, "Bitte zuerst die E-Mail-Adresse eingeben.");
      if (emailEl) emailEl.focus();
      return;
    }
    try {
      var begin = await postJSON("/login/passkey/begin", { email: email });
      var assertion = await navigator.credentials.get({ publicKey: decodeRequest(begin.options) });
      var done = await postJSON("/login/passkey/finish", {
        challenge_id: begin.challenge_id,
        credential: encodeCredential(assertion)
      });
      window.location.assign(done.next || "/dashboard");
    } catch (err) {
      showError(document, err.message || "Passkey-Anmeldung fehlgeschlagen.");
    }
  }

  async function register(button) {
    var beginURL = button.getAttribute("data-begin");
    var finishURL = button.getAttribute("data-finish");
    var redirect = button.getAttribute("data-redirect");
    try {
      var begin = await postJSON(beginURL, {});
      var credential = await navigator.credentials.create({ publicKey: decodeCreation(begin.options) });
      await postJSON(finishURL, {
        challenge_id: begin.challenge_id,
        credential: encodeCredential(credential),
        name: "Passkey"
      });
      if (redirect) {
        window.location.assign(redirect);
      } else {
        window.location.reload();
      }
    } catch (err) {
      showError(button.closest("section") || document, err.message || "Passkey konnte nicht eingerichtet werden.");
    }
  }

  document.querySelectorAll("[data-passkey-login]").forEach(function (el) {
    el.addEventListener("click", function (e) {
      e.preventDefault();
      login();
    });
  });

  document.querySelectorAll("[data-passkey-register]").forEach(function (el) {
    el.addEventListener("click", function (e) {
      e.preventDefault();
      register(el);
    });
  });
})();
