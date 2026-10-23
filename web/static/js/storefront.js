(function () {
  "use strict";

  function keyFor(slug) {
    return "tk_cart_" + (slug || "default");
  }

  function readCart(slug) {
    try {
      var raw = window.localStorage.getItem(keyFor(slug));
      if (!raw) return [];
      var parsed = JSON.parse(raw);
      if (!Array.isArray(parsed)) return [];
      return parsed
        .filter(function (l) {
          return l && typeof l.id === "string" && Number.isInteger(l.qty) && l.qty > 0;
        })
        .map(function (l) {
          return {
            id: l.id,
            name: String(l.name || ""),
            priceCents: Number(l.priceCents) || 0,
            qty: Math.min(l.qty, 20)
          };
        });
    } catch (err) {
      return [];
    }
  }

  function writeCart(slug, lines) {
    try {
      window.localStorage.setItem(keyFor(slug), JSON.stringify(lines));
    } catch (err) {
      /* storage may be unavailable in private mode */
    }
  }

  function centsToMoney(cents) {
    var value = (cents / 100).toFixed(2);
    return "€" + value;
  }

  function priceFromText(text) {
    var match = String(text).match(/(\d+)[.,](\d{1,2})/);
    if (!match) return 0;
    var frac = (match[2] + "00").slice(0, 2);
    return parseInt(match[1], 10) * 100 + parseInt(frac, 10);
  }

  function total(lines) {
    return lines.reduce(function (sum, line) {
      return sum + line.priceCents * line.qty;
    }, 0);
  }

  function count(lines) {
    return lines.reduce(function (sum, line) {
      return sum + line.qty;
    }, 0);
  }

  function addLine(slug, item) {
    var lines = readCart(slug);
    var existing = null;
    for (var i = 0; i < lines.length; i++) {
      if (lines[i].id === item.id) {
        existing = lines[i];
        break;
      }
    }
    if (existing) {
      existing.qty = Math.min(existing.qty + 1, 20);
    } else {
      lines.push({ id: item.id, name: item.name, priceCents: item.priceCents, qty: 1 });
    }
    writeCart(slug, lines);
    return lines;
  }

  function changeQty(slug, id, delta) {
    var lines = readCart(slug);
    lines = lines
      .map(function (line) {
        if (line.id === id) line.qty = Math.min(line.qty + delta, 20);
        return line;
      })
      .filter(function (line) {
        return line.qty > 0;
      });
    writeCart(slug, lines);
    return lines;
  }

  function el(tag, className, text) {
    var node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function renderLines(slug, lines) {
    var lists = document.querySelectorAll("[data-cart-lines]");
    for (var i = 0; i < lists.length; i++) {
      var list = lists[i];
      list.textContent = "";
      for (var j = 0; j < lines.length; j++) {
        var line = lines[j];
        var row = el("li", "flex items-center justify-between gap-2 py-2 text-sm");
        var label = el("span", "min-w-0 flex-1 truncate", line.qty + "× " + line.name);
        var controls = el("span", "flex items-center gap-2");
        var minus = el("button", "rounded border border-stone-300 px-2 text-sm", "−");
        minus.type = "button";
        minus.setAttribute("data-cart-dec", line.id);
        minus.setAttribute("aria-label", "Remove one " + line.name);
        var plus = el("button", "rounded border border-stone-300 px-2 text-sm", "+");
        plus.type = "button";
        plus.setAttribute("data-cart-inc", line.id);
        plus.setAttribute("aria-label", "Add one " + line.name);
        var price = el("span", "w-16 text-right text-stone-500", centsToMoney(line.priceCents * line.qty));
        controls.appendChild(minus);
        controls.appendChild(plus);
        row.appendChild(label);
        row.appendChild(controls);
        row.appendChild(price);
        list.appendChild(row);
      }
    }

    var empty = document.querySelectorAll("[data-cart-empty]");
    for (var k = 0; k < empty.length; k++) {
      empty[k].hidden = lines.length !== 0;
    }
    var totals = document.querySelectorAll("[data-cart-total]");
    for (var m = 0; m < totals.length; m++) {
      totals[m].textContent = centsToMoney(total(lines));
    }
    var counts = document.querySelectorAll("[data-cart-count]");
    for (var n = 0; n < counts.length; n++) {
      counts[n].textContent = count(lines) ? String(count(lines)) : "";
    }
  }

  function syncCheckout(slug, lines) {
    var form = document.getElementById("checkout-form");
    if (!form) return;
    var input = form.querySelector('input[name="cart"]');
    if (input) {
      input.value = JSON.stringify(
        lines.map(function (line) {
          return { id: line.id, qty: line.qty };
        })
      );
    }
    var submit = form.querySelector("[data-checkout-submit]");
    if (submit) submit.disabled = lines.length === 0;
  }

  function update(slug) {
    var lines = readCart(slug);
    renderLines(slug, lines);
    syncCheckout(slug, lines);
  }

  document.addEventListener("click", function (event) {
    var target = event.target.closest("[data-add],[data-cart-inc],[data-cart-dec],[data-cart-clear]");
    if (!target) return;
    var root = document.querySelector("[data-page]");
    var slug = root ? root.getAttribute("data-slug") : "";
    event.preventDefault();

    if (target.hasAttribute("data-add")) {
      var lines = addLine(slug, {
        id: target.getAttribute("data-add"),
        name: target.getAttribute("data-name") || "",
        priceCents: priceFromText(target.getAttribute("data-price") || "0")
      });
      renderLines(slug, lines);
      syncCheckout(slug, lines);
      return;
    }
    if (target.hasAttribute("data-cart-inc")) {
      updateAfter(changeQty(slug, target.getAttribute("data-cart-inc"), 1), slug);
      return;
    }
    if (target.hasAttribute("data-cart-dec")) {
      updateAfter(changeQty(slug, target.getAttribute("data-cart-dec"), -1), slug);
      return;
    }
    if (target.hasAttribute("data-cart-clear")) {
      writeCart(slug, []);
      update(slug);
    }
  });

  function updateAfter(lines, slug) {
    renderLines(slug, lines);
    syncCheckout(slug, lines);
  }

  function setupFulfilment() {
    var form = document.getElementById("checkout-form");
    if (!form) return;
    var fields = form.querySelector("[data-delivery-fields]");
    var radios = form.querySelectorAll('input[name="fulfilment"]');
    var saved = form.querySelector('select[name="address_id"]');
    var addressInputs = ["recipient", "delivery_phone", "line1", "postcode", "city"];
    function apply() {
      var selected = form.querySelector('input[name="fulfilment"]:checked');
      var delivery = selected && selected.value === "delivery";
      if (fields) fields.hidden = !delivery;
      var useNew = !saved || saved.value === "";
      for (var i = 0; i < addressInputs.length; i++) {
        var input = form.querySelector('[name="' + addressInputs[i] + '"]');
        if (input) input.required = !!delivery && useNew;
      }
    }
    for (var i = 0; i < radios.length; i++) {
      radios[i].addEventListener("change", apply);
    }
    if (saved) saved.addEventListener("change", apply);
    apply();

    form.addEventListener("submit", function (event) {
      var root = document.querySelector("[data-page]");
      var slug = root ? root.getAttribute("data-slug") : "";
      var lines = readCart(slug);
      if (lines.length === 0) {
        event.preventDefault();
        return;
      }
      var input = form.querySelector('input[name="cart"]');
      if (input) {
        input.value = JSON.stringify(
          lines.map(function (line) {
            return { id: line.id, qty: line.qty };
          })
        );
      }
    });
  }

  document.addEventListener("DOMContentLoaded", function () {
    var root = document.querySelector("[data-page]");
    if (!root) return;
    var slug = root.getAttribute("data-slug") || "";
    if (root.getAttribute("data-page") === "order") {
      writeCart(slug, []);
      return;
    }
    update(slug);
    setupFulfilment();
  });
})();
