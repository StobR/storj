// Copyright (C) 2021 Storj Labs, Inc.
// See LICENSE for copying information.

<template>
    <base-table >
        <thead slot="head">
            <tr>
                <th class="align-left">SATELLITE</th>
                <th>MONTH 1-3</th>
                <th>MONTH 4-6</th>
                <th>MONTH 7-9</th>
            </tr>
        </thead>
        <tbody slot="body">
            <tr class="table-item" v-for="(heldHistoryItem, index) in heldHistory" :key="index">
                <th class="align-left">
                    <p class="table-item__name">{{ heldHistoryItem.satelliteAddress }}</p>
                    <p class="table-item__months">{{ heldHistoryItem.monthsCount }}</p>
                </th>
                <th>{{ heldHistoryItem.firstQuarter | centsToDollars }}</th>
                <th>{{ heldHistoryItem.secondQuarter | centsToDollars }}</th>
                <th>{{ heldHistoryItem.thirdQuarter | centsToDollars }}</th>
            </tr>
        </tbody>
    </base-table>
</template>

<script lang="ts">
import { Component, Prop, Vue } from 'vue-property-decorator';

import BaseTable from '@/app/components/common/BaseTable.vue';

import { HeldAmountSummary } from '@/payouts';

@Component({
    components: {
        BaseTable,
    },
})
export default class HeldHistory extends Vue {
    @Prop({ default: () => [] })
    public heldHistory: HeldAmountSummary[];
}
</script>

<style lang="scss" scoped>
    .table-item {

        &__name {
            font-family: 'font_regular', sans-serif;
            font-size: 14px;
            color: var(--regular-text-color);
            max-width: calc(100% - 40px);
            word-break: break-word;
        }

        &__months {
            font-family: 'font_regular', sans-serif;
            font-size: 11px;
            color: #9b9db1;
            margin-top: 3px;
        }
    }
</style>
