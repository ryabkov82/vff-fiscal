#!/usr/bin/env perl
use v5.14;
use strict;
use warnings;

use lib '/app/data/pay_systems/lib';

use Core::Base;
use Core::Config;
use Core::Utils qw(encode_json);
use JSON::PP ();
use SHM qw(:all);
use VFFFiscal::AdapterConfig qw(normalize_non_empty_scalar);

my $MODULE = 'srv_customlab_nalog';
my $shm;

sub shm {
    $shm //= SHM->new(skip_check_auth => 1);
    return $shm;
}

sub fail {
    my ($message) = @_;
    print encode_json({ status => 500, error => $message }) if defined wantarray;
    die "$message\n";
}

sub load_pay_systems_data {
    shm();
    my $config_service = get_service('config', _id => 'pay_systems')
        or fail('pay_systems config service is missing');
    my $all_config = $config_service->get_data;
    fail('pay_systems config data is missing') unless $all_config && ref $all_config eq 'HASH';
    return $all_config;
}

sub module_config {
    my ($all_config) = @_;
    my $cfg = $all_config->{$MODULE};
    return {} unless $cfg && ref $cfg eq 'HASH';
    return {%$cfg};
}

sub safe_status {
    my ($all_config) = @_;
    my %cfg = %{ module_config($all_config) };

    my $enabled = $cfg{enabled} ? JSON::PP::true : JSON::PP::false;
    my $need_update_to_defined = defined $cfg{need_update_to} ? JSON::PP::true : JSON::PP::false;
    my $client_token_present = normalize_non_empty_scalar($cfg{client_token})
        ? JSON::PP::true
        : JSON::PP::false;
    my $service_name_present = normalize_non_empty_scalar($cfg{service_name})
        ? JSON::PP::true
        : JSON::PP::false;
    my $pay_systems_present = defined $cfg{pay_systems}
        && (
            ( ref $cfg{pay_systems} eq 'ARRAY' && @{ $cfg{pay_systems} } )
            || ( !ref $cfg{pay_systems} && length $cfg{pay_systems} )
        )
        ? JSON::PP::true
        : JSON::PP::false;

    return {
        enabled                => $enabled,
        version                => defined $cfg{version} ? "$cfg{version}" : '',
        need_update_to_defined => $need_update_to_defined,
        client_token_present   => $client_token_present,
        service_name_present   => $service_name_present,
        pay_systems_present    => $pay_systems_present,
    };
}

sub write_pay_systems_data {
    my ($all_config) = @_;
    Core::Config::set_value('pay_systems', $all_config);
    shm()->commit;
}

sub cmd_status {
    my $all_config = load_pay_systems_data();
    print encode_json(safe_status($all_config));
}

sub cmd_clear_update_marker {
    my $all_config = load_pay_systems_data();
    my %cfg = %{ module_config($all_config) };
    fail('module config is missing') unless %cfg || exists $all_config->{$MODULE};

    $all_config->{$MODULE} = \%cfg unless ref $all_config->{$MODULE} eq 'HASH';
    delete $all_config->{$MODULE}{need_update_to};

    write_pay_systems_data($all_config);

    my $after = load_pay_systems_data();
    my %after_cfg = %{ module_config($after) };
    fail('need_update_to is still defined') if defined $after_cfg{need_update_to};

    print encode_json({ status => 200, need_update_to_defined => JSON::PP::false });
}

sub cmd_set_enabled {
    my ($value) = @_;
    fail('set-enabled requires 0 or 1') unless defined $value && $value =~ /\A[01]\z/;

    my $all_config = load_pay_systems_data();
    my %cfg = %{ module_config($all_config) };
    fail('module config is missing') unless exists $all_config->{$MODULE};

    $cfg{enabled} = $value ? 1 : 0;
    delete $cfg{need_update_to};
    $all_config->{$MODULE} = \%cfg;

    write_pay_systems_data($all_config);

    my $after = load_pay_systems_data();
    my %after_cfg = %{ module_config($after) };
    fail('need_update_to is still defined') if defined $after_cfg{need_update_to};
    fail('enabled value mismatch') if ($after_cfg{enabled} ? 1 : 0) != ($value ? 1 : 0);

    print encode_json({
        status   => 200,
        enabled  => $after_cfg{enabled} ? JSON::PP::true : JSON::PP::false,
        need_update_to_defined => JSON::PP::false,
    });
}

my $cmd = shift @ARGV // 'status';
if ($cmd eq 'status') {
    cmd_status();
}
elsif ($cmd eq 'clear-update-marker') {
    cmd_clear_update_marker();
}
elsif ($cmd eq 'set-enabled') {
    cmd_set_enabled(shift @ARGV);
}
else {
    fail("unsupported command: $cmd");
}
