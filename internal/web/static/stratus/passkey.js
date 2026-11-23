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

  // The options as the browser takes them, read afresh each time: a call the
  // browser refused leaves the decoded copy spent.
  function optionsOf(form, creating) {
    var options = JSON.parse(form.getAttribute("data-options")).publicKey;
    options.challenge = bytes(options.challenge);
    if (creating) {
      options.user.id = bytes(options.user.id);
      options.excludeCredentials = descriptors(options.excludeCredentials);
    } else {
      options.allowCredentials = descriptors(options.allowCredentials);
    }
    return options;
  }

  function send(form, cred) {
    form.elements.credential.value = JSON.stringify(encode(cred));
    form.submit();
  }

  document.querySelectorAll("form[data-passkey]").forEach(function (form) {
    var creating = form.getAttribute("data-passkey") === "create";
    // The sign-in's waiting request: offered by the browser or a password
    // manager in the username field's autofill, with no button to press. A
    // browser holds one request at a time, so the button aborts it first.
    var waiting = null;
    form.classList.remove("d-none");

    if (!creating && PublicKeyCredential.isConditionalMediationAvailable) {
      PublicKeyCredential.isConditionalMediationAvailable().then(function (available) {
        if (!available) {
          return;
        }
        waiting = new AbortController();
        navigator.credentials
          .get({ publicKey: optionsOf(form, false), mediation: "conditional", signal: waiting.signal })
          .then(
            function (cred) {
              send(form, cred);
            },
            function () {
              // Aborted for the button, or nothing chosen: the button is
              // still there.
            }
          );
      });
    }

    form.addEventListener("submit", function (event) {
      if (form.elements.credential.value) {
        return;
      }
      event.preventDefault();
      if (!form.reportValidity()) {
        return;
      }
      if (waiting) {
        waiting.abort();
        waiting = null;
      }
      var options = optionsOf(form, creating);
      var ask = creating
        ? navigator.credentials.create({ publicKey: options })
        : navigator.credentials.get({ publicKey: options });
      // Cancelled, or no passkey there: the page that was is still the page,
      // and the button asks again.
      ask.then(function (cred) {
        send(form, cred);
      }, function () {});
    });
  });
})();
