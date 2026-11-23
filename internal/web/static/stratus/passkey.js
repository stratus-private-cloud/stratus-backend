// Passkeys (#532): the one thing on these pages a form cannot do alone. The
// page renders the options the server chose into data-options; this hands
// them to the browser, writes what it answers into the form's credential
// field as the standard JSON of a PublicKeyCredential, and submits the form.
// Nothing is fetched: the answer is a POST of the form, like every other one.
//
// It degrades by construction, as copy.js does. The forms ship hidden and
// this is what shows them, so a browser with no script or no WebAuthn sees
// the password and nothing else.
(function () {
  if (!window.PublicKeyCredential || !navigator.credentials) {
    return;
  }

  function bytes(text) {
    var b64 = text.replace(/-/g, "+").replace(/_/g, "/");
    var raw = atob(b64 + "===".slice((b64.length + 3) % 4));
    var out = new Uint8Array(raw.length);
    for (var i = 0; i < raw.length; i++) {
      out[i] = raw.charCodeAt(i);
    }
    return out.buffer;
  }

  function text(buffer) {
    var raw = "";
    new Uint8Array(buffer).forEach(function (b) {
      raw += String.fromCharCode(b);
    });
    return btoa(raw).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
  }

  function descriptors(list) {
    return (list || []).map(function (d) {
      return Object.assign({}, d, { id: bytes(d.id) });
    });
  }

  // toJSON where the browser has it, and the same shape by hand where not.
  function encode(cred) {
    if (typeof cred.toJSON === "function") {
      return cred.toJSON();
    }
    var r = cred.response;
    var out = {
      id: cred.id,
      rawId: text(cred.rawId),
      type: cred.type,
      authenticatorAttachment: cred.authenticatorAttachment || undefined,
      clientExtensionResults: cred.getClientExtensionResults(),
      response: { clientDataJSON: text(r.clientDataJSON) },
    };
    if (r.attestationObject) {
      out.response.attestationObject = text(r.attestationObject);
      out.response.transports = r.getTransports ? r.getTransports() : [];
    } else {
      out.response.authenticatorData = text(r.authenticatorData);
      out.response.signature = text(r.signature);
      if (r.userHandle) {
        out.response.userHandle = text(r.userHandle);
      }
    }
    return out;
  }

  document.querySelectorAll("[data-passkey-missing]").forEach(function (p) {
    p.classList.add("d-none");
  });

  document.querySelectorAll("form[data-passkey]").forEach(function (form) {
    var options = JSON.parse(form.getAttribute("data-options")).publicKey;
    var creating = form.getAttribute("data-passkey") === "create";
    form.classList.remove("d-none");
    form.addEventListener("submit", function (event) {
      if (form.elements.credential.value) {
        return;
      }
      event.preventDefault();
      if (!form.reportValidity()) {
        return;
      }
      options.challenge = bytes(options.challenge);
      var ask;
      if (creating) {
        options.user.id = bytes(options.user.id);
        options.excludeCredentials = descriptors(options.excludeCredentials);
        ask = navigator.credentials.create({ publicKey: options });
      } else {
        options.allowCredentials = descriptors(options.allowCredentials);
        ask = navigator.credentials.get({ publicKey: options });
      }
      ask.then(
        function (cred) {
          form.elements.credential.value = JSON.stringify(encode(cred));
          form.submit();
        },
        function () {
          // Cancelled, or no passkey there: the page that was is still the
          // page, and a reload asks again with a fresh challenge.
          options = JSON.parse(form.getAttribute("data-options")).publicKey;
        }
      );
    });
  });
})();
