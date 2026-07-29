(() => {
  "use strict";

  const countedFields = document.querySelectorAll("[data-counted]");

  for (const field of countedFields) {
    const counter = field.parentElement.querySelector("[data-character-count]");
    if (!counter) {
      continue;
    }

    const updateCount = () => {
      counter.textContent = String(field.value.length);
    };

    field.addEventListener("input", updateCount);
    updateCount();
  }

  for (const form of document.querySelectorAll("form")) {
    if (form.matches("[data-review-form]")) {
      const reflections = Array.from(
        form.querySelectorAll(
          'textarea[name="biggest_mistake"], textarea[name="done_well"], textarea[name="next_game"]',
        ),
      );
      for (const reflection of reflections) {
        reflection.addEventListener("input", () => {
          for (const field of reflections) {
            field.setCustomValidity("");
          }
        });
      }
    }

    form.addEventListener("submit", (event) => {
      if (form.matches("[data-review-form]")) {
        const reflections = Array.from(
          form.querySelectorAll(
            'textarea[name="biggest_mistake"], textarea[name="done_well"], textarea[name="next_game"]',
          ),
        );
        if (reflections.length > 0 && reflections.every((field) => !field.value.trim())) {
          event.preventDefault();
          reflections[0].setCustomValidity("Add at least one short reflection before saving.");
          reflections[0].reportValidity();
          return;
        }
      }

      const confirmation = form.dataset.confirm;
      if (confirmation && !window.confirm(confirmation)) {
        event.preventDefault();
        return;
      }

      const submitter = form.querySelector('button[type="submit"]');
      if (!submitter || submitter.disabled) {
        return;
      }

      submitter.disabled = true;
      submitter.setAttribute("aria-disabled", "true");
    });
  }

  for (const form of document.querySelectorAll("[data-training-form]")) {
    const metric = form.querySelector("[data-target-metric]");
    const targetValue = form.querySelector("[data-target-value]");
    const startDate = form.querySelector("[data-start-date]");
    const endDate = form.querySelector("[data-end-date]");

    if (metric && targetValue) {
      const updateTargetRequirement = () => {
        targetValue.required = metric.value !== "";
      };
      metric.addEventListener("change", updateTargetRequirement);
      updateTargetRequirement();
    }

    if (startDate && endDate) {
      const updateEndDateMinimum = () => {
        endDate.min = startDate.value;
      };
      startDate.addEventListener("change", updateEndDateMinimum);
      updateEndDateMinimum();
    }
  }
})();
