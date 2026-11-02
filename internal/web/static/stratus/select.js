// Select-all for the listing's checkboxes (#295). It ticks the rows already
// loaded and no others: a selection does not survive paging and is not
// promised to. The box ships hidden, so with no script there is no box that
// does nothing.
document.querySelectorAll("[data-selects]").forEach(function (all) {
  var form = all.getAttribute("data-selects");
  all.classList.remove("d-none");
  all.addEventListener("change", function () {
    document.querySelectorAll('input[form="' + form + '"]').forEach(function (box) {
      box.checked = all.checked;
    });
  });
});
