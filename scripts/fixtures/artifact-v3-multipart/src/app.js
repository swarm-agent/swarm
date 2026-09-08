import { plans } from './plans.js'

const root = document.querySelector('#plans')
let selected = ''

function render() {
  root.replaceChildren(...plans.map((plan) => {
    const card = document.createElement('article')
    card.className = 'plan'
    card.dataset.plan = plan.id
    card.dataset.selected = String(plan.id === selected)
    card.innerHTML = `<h3>${plan.name}</h3><p>${plan.price} / month</p>`
    return card
  }))
}

function selectPlan(id) {
  if (!plans.some((plan) => plan.id === id)) throw new Error(`unknown plan: ${id}`)
  selected = id
  render()
}

document.addEventListener('click', (event) => {
  const trigger = event.target.closest('[data-plan]')
  if (trigger) selectPlan(trigger.dataset.plan)
})

window.artifactFixture = { selectPlan, selectedPlan: () => selected }
render()
