<script setup lang="ts">
import { navDiscover, navOperate, navPlan, type NavItem } from '../router'

const areas: { name: string; blurb: string; pages: NavItem[] }[] = [
  { name: 'Operate', blurb: "What the observatory is doing, and tonight's plan.", pages: navOperate },
  { name: 'Plan', blurb: 'Everything you edit. Changes apply at the end of the current exposure.', pages: navPlan },
  { name: 'Discover', blurb: 'Read-only views that help you pick what to image next.', pages: navDiscover },
]

const flow = [
  { title: 'You edit here', text: 'Any page in Plan. The new value shows at once.' },
  { title: 'Queued if the PC is away', text: 'Kept in order and sent when it answers.' },
  { title: 'Applied at the end of the sub', text: 'The top bar counts down to it. Then the scheduler re-plans.' },
  { title: 'Recorded with undo', text: 'History keeps the before and after.' },
]
</script>

<template>
  <main class="page" style="gap: 2rem">
    <div style="display: flex; flex-direction: column; gap: 0.5rem; max-width: 68ch">
      <h1 style="margin: 0; font-size: 1.75rem; font-weight: 600; line-height: 1.25; text-wrap: balance">Observatory scheduler</h1>
      <p style="margin: 0; font-size: 0.9375rem" class="muted">
        The web editor for the forked Target Scheduler. When web editing is switched on at the observatory, this is the only
        place targets, goals and templates are edited, and every change lands in History, where you can undo it.
      </p>
    </div>

    <section aria-labelledby="areas-h" style="display: flex; flex-direction: column; gap: 0.75rem">
      <h2 id="areas-h" style="margin: 0; font-size: 1rem; font-weight: 600">Three areas</h2>
      <div class="areas">
        <div v-for="a in areas" :key="a.name" class="area">
          <div>
            <div style="font-weight: 600; font-size: 1.0625rem">{{ a.name }}</div>
            <div class="small muted">{{ a.blurb }}</div>
          </div>
          <ul class="area-list">
            <li v-for="p in a.pages" :key="p.name">
              <RouterLink :to="{ name: p.name }" class="area-link">
                <span style="display: flex; flex-direction: column">
                  <span style="font-weight: 600; font-size: 0.875rem">{{ p.label }}</span>
                  <span class="xsmall muted">{{ p.note }}</span>
                </span>
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="var(--muted-foreground)" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m9 6 6 6-6 6" /></svg>
              </RouterLink>
            </li>
          </ul>
        </div>
      </div>
    </section>

    <section aria-labelledby="flow-h" style="display: flex; flex-direction: column; gap: 0.75rem">
      <h2 id="flow-h" style="margin: 0; font-size: 1rem; font-weight: 600">How an edit travels</h2>
      <ol class="flow">
        <li v-for="f in flow" :key="f.title">
          <div style="font-weight: 600">{{ f.title }}</div>
          <div class="muted">{{ f.text }}</div>
        </li>
      </ol>
    </section>
  </main>
</template>

<style scoped>
.areas {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 19rem), 1fr));
  gap: 1rem;
  align-items: start;
}
.area {
  border: 1px solid var(--border);
  border-radius: 14px;
  background: var(--card);
  padding: 1.25rem;
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}
.area-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
}
.area-list li {
  border-top: 1px solid var(--border);
}
.area-link {
  width: 100%;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 0.75rem;
  min-height: 2.75rem;
  padding: 0.375rem 0;
  text-decoration: none;
}
.flow {
  list-style: none;
  margin: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(100%, 13rem), 1fr));
  gap: 0.75rem;
  font-size: 0.8125rem;
}
.flow li {
  border: 1px solid var(--border);
  border-radius: 0.625rem;
  padding: 0.75rem 1rem;
}
</style>
