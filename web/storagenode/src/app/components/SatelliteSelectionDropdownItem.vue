// Copyright (C) 2020 Storj Labs, Inc.
// See LICENSE for copying information.

<template>
    <button
        :name="`Choose ${satellite.url} satellite`"
        class="satellite-choice"
        @click.stop="onSatelliteClick"
    >
        <DisqualificationIcon
            class="satellite-choice__image"
            v-if="satellite.disqualified"
            alt="disqualified image"
        />
        <SuspensionIcon
            class="satellite-choice__image"
            v-if="satellite.suspended && !satellite.disqualified"
            alt="suspended image"
        />
        <p
            class="satellite-choice__name"
            :class="{
                disqualified: satellite.disqualified,
                suspended: satellite.suspended,
                'with-id-button': isNameShown,
                'with-copy-button': !isNameShown
            }"
        >
            {{ isNameShown ? satellite.url : satellite.id }}
        </p>
        <div class="satellite-choice__right-area">
            <button
                name="Show Satellite ID"
                class="satellite-choice__right-area__button"
                @click.stop.prevent="toggleSatelliteView"
                v-if="isNameShown"
            >
                <EyeIcon />
                <p class="satellite-choice__right-area__button__text">ID</p>
            </button>
            <div class="row" v-else >
                <button
                    name="Copy Satellite ID"
                    class="satellite-choice__right-area__button copy-button"
                    @click.stop="() => {}"
                    v-clipboard:copy="satellite.id"
                >
                    <CopyIcon />
                </button>
                <button
                    name="Show Satellite Name"
                    class="satellite-choice__right-area__button"
                    @click.stop.prevent="toggleSatelliteView"
                >
                    <EyeIcon />
                    <p class="satellite-choice__right-area__button__text">Name</p>
                </button>
            </div>
        </div>
    </button>
</template>

<script lang="ts">
import { Component, Prop, Vue } from 'vue-property-decorator';

import CopyIcon from '@/../static/images/Copy.svg';
import DisqualificationIcon from '@/../static/images/disqualify.svg';
import EyeIcon from '@/../static/images/Eye.svg';
import SuspensionIcon from '@/../static/images/suspend.svg';

import { SatelliteInfo } from '@/storagenode/sno/sno';

@Component({
    components: {
        DisqualificationIcon,
        SuspensionIcon,
        CopyIcon,
        EyeIcon,
    },
})
export default class SatelliteSelectionDropdownItem extends Vue {
    @Prop({default: () => new SatelliteInfo()})
    public readonly satellite: SatelliteInfo;

    /**
     * Indicates if name or id should be shown.
     */
    public isNameShown = true;

    /**
     * Toggles between name and id view.
     */
    public toggleSatelliteView(): void {
        this.isNameShown = !this.isNameShown;
    }

    /**
     * Emits action that selects satellite and fetches related information.
     */
    public onSatelliteClick(): void {
        this.$emit('onSatelliteClick', this.satellite.id);
    }
}
</script>

<style scoped lang="scss">
    .satellite-choice {
        position: relative;
        display: flex;
        width: calc(100% - 36px);
        align-items: center;
        justify-content: space-between;
        margin-left: 8px;
        border-radius: 12px;
        padding: 10px;

        &__image {
            position: absolute;
            top: 50%;
            left: 10px;
            transform: translateY(-50%);
        }

        &__name {
            font-size: 14px;
            line-height: 21px;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }

        &:hover {
            background-color: var(--satellite-selection-hover-background-color);
            cursor: pointer;
            color: var(--regular-text-color);
        }

        &__right-area {

            &__button {
                display: flex;
                align-items: center;
                justify-content: center;
                background: var(--button-background-color);
                border-radius: 5px;
                height: 30px;
                padding: 0 10px;
                cursor: pointer;
                font-family: 'font_medium', sans-serif;
                font-size: 13px;
                color: #9cabbe;
                border: transparent;

                &__text {
                    margin-left: 6.75px;
                }

                &:hover {
                    background-color: #e4ebfc;
                    cursor: pointer;
                    color: #133e9c;

                    .svg {

                        path {
                            fill: #133e9c !important;
                        }
                    }
                }
            }
        }
    }

    .disqualified,
    .suspended {
        margin-left: 24px;
    }

    .copy-button {
        margin-right: 8px;
    }

    .row {
        display: flex;
    }

    .with-id-button {
        width: calc(100% - 90px);
    }

    .with-copy-button {
        width: calc(100% - 140px);
    }
</style>
